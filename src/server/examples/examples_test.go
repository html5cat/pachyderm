package examples

import (
	"bytes"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pachyderm/pachyderm/src/client"
	pfsclient "github.com/pachyderm/pachyderm/src/client/pfs"
	"github.com/pachyderm/pachyderm/src/client/pkg/require"
)

func getPachClient(t testing.TB) *client.APIClient {
	client, err := client.NewFromAddress("0.0.0.0:30650")
	require.NoError(t, err)
	return client
}

func TestExampleTensorFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	c := getPachClient(t)
	t.Parallel()

	cwd, err := os.Getwd()
	require.NoError(t, err)
	exampleDir := filepath.Join(cwd, "../../../doc/examples/tensor_flow")
	cmd := exec.Command("make", "test")
	cmd.Dir = exampleDir
	_, err = cmd.CombinedOutput()
	require.NoError(t, err)

	commitInfos, err := c.ListCommit(
		[]*pfsclient.Commit{{
			Repo: &pfsclient.Repo{"GoT_scripts"},
		}},
		nil,
		client.CommitTypeRead,
		client.CommitStatusAll,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, 1, len(commitInfos))
	inputCommitID := commitInfos[0].Commit.ID

	// Wait until the GoT_generate job has finished
	commitInfos, err = c.FlushCommit([]*pfsclient.Commit{client.NewCommit("GoT_scripts", inputCommitID)}, nil)
	require.NoError(t, err)
	require.Equal(t, 3, len(commitInfos))

	repos := []interface{}{"GoT_train", "GoT_generate", "GoT_scripts"}
	var generateCommitID string
	for _, commitInfo := range commitInfos {
		require.EqualOneOf(t, repos, commitInfo.Commit.Repo.Name)
		if commitInfo.Commit.Repo.Name == "GoT_generate" {
			generateCommitID = commitInfo.Commit.ID
		}
	}

	// Make sure the final output is non zero
	var buffer bytes.Buffer
	require.NoError(t, c.GetFile("GoT_generate", generateCommitID, "new_script.txt", 0, 0, "", false, nil, &buffer))
	if buffer.Len() < 100 {
		t.Fatalf("Output GoT script is too small (has len=%v)", buffer.Len())
	}
	require.NoError(t, c.DeleteRepo("GoT_generate", false))
	require.NoError(t, c.DeleteRepo("GoT_train", false))
	require.NoError(t, c.DeleteRepo("GoT_scripts", false))
}

func TestWordCount(t *testing.T) {

	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}
	t.Parallel()
	c := getPachClient(t)

	exampleDir := "../../../doc/examples/word_count"
	newURL := "https://news.ycombinator.com/newsfaq.html"
	oldURL := "https://en.wikipedia.org/wiki/Main_Page"
	rawInputPipelineManifest, err := ioutil.ReadFile(filepath.Join(exampleDir, "inputPipeline.json"))
	require.NoError(t, err)
	// Need to choose a page w much fewer links to make this pass on CI
	inputPipelineManifest := strings.Replace(string(rawInputPipelineManifest), oldURL, newURL, 1)

	cmd := exec.Command("pachctl", "create-pipeline")
	cmd.Stdin = strings.NewReader(inputPipelineManifest)
	cmd.Dir = exampleDir
	_, err = cmd.CombinedOutput()
	require.NoError(t, err)

	cmd = exec.Command("pachctl", "run-pipeline", "wordcount_input")
	cmd.Dir = exampleDir
	_, err = cmd.Output()
	require.NoError(t, err)

	cmd = exec.Command("docker", "build", "-t", "wordcount-map", ".")
	cmd.Dir = exampleDir
	_, err = cmd.CombinedOutput()
	require.NoError(t, err)

	wordcountMapPipelineManifest, err := ioutil.ReadFile(filepath.Join(exampleDir, "mapPipeline.json"))
	require.NoError(t, err)

	cmd = exec.Command("pachctl", "create-pipeline")
	cmd.Stdin = strings.NewReader(string(wordcountMapPipelineManifest))
	cmd.Dir = exampleDir
	_, err = cmd.Output()
	require.NoError(t, err)

	// Flush Commit can't help us here since there are no inputs
	// So we block on ListCommit
	commitInfos, err := c.ListCommit(
		[]*pfsclient.Commit{{
			Repo: &pfsclient.Repo{"wordcount_input"},
		}},
		nil,
		client.CommitTypeRead,
		client.CommitStatusNormal,
		true,
	)
	require.NoError(t, err)
	require.Equal(t, 1, len(commitInfos))
	inputCommit := commitInfos[0].Commit
	commitInfos, err = c.FlushCommit([]*pfsclient.Commit{inputCommit}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, len(commitInfos))

	commitInfos, err = c.ListCommit(
		[]*pfsclient.Commit{{
			Repo: &pfsclient.Repo{"wordcount_map"},
		}},
		nil,
		client.CommitTypeRead,
		client.CommitStatusNormal,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, 1, len(commitInfos))

	var buffer bytes.Buffer
	require.NoError(t, c.GetFile(commitInfos[0].Commit.Repo.Name, commitInfos[0].Commit.ID, "are", 0, 0, "", false, nil, &buffer))
	lines := strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n")
	// Should see # lines output == # pods running job
	// This should be just one with default deployment
	require.Equal(t, 1, len(lines))

	wordcountReducePipelineManifest, err := ioutil.ReadFile(filepath.Join(exampleDir, "reducePipeline.json"))
	require.NoError(t, err)

	cmd = exec.Command("pachctl", "create-pipeline")
	cmd.Stdin = strings.NewReader(string(wordcountReducePipelineManifest))
	cmd.Dir = exampleDir
	_, err = cmd.Output()
	require.NoError(t, err)

	commitInfos, err = c.FlushCommit([]*pfsclient.Commit{inputCommit}, nil)
	require.NoError(t, err)
	require.Equal(t, 3, len(commitInfos))

	commitInfos, err = c.ListCommit(
		[]*pfsclient.Commit{{
			Repo: &pfsclient.Repo{"wordcount_reduce"},
		}},
		nil,
		client.CommitTypeRead,
		client.CommitStatusNormal,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, 1, len(commitInfos))
	buffer.Reset()
	require.NoError(t, c.GetFile("wordcount_reduce", commitInfos[0].Commit.ID, "morning", 0, 0, "", false, nil, &buffer))
	lines = strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n")
	require.Equal(t, 1, len(lines))

	fileInfos, err := c.ListFile("wordcount_reduce", commitInfos[0].Commit.ID, "", "", false, nil, false)
	require.NoError(t, err)

	if len(fileInfos) < 100 {
		t.Fatalf("Word count result is too small. Should have counted a bunch of words. Only counted %v:\n%v\n", len(fileInfos), fileInfos)
	}
}

func TestFruitStand(t *testing.T) {

	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	c := getPachClient(t)
	t.Parallel()

	require.NoError(t, c.CreateRepo("data"))
	repoInfos, err := c.ListRepo(nil)
	require.NoError(t, err)
	var repoNames []interface{}
	for _, repoInfo := range repoInfos {
		repoNames = append(repoNames, repoInfo.Repo.Name)
	}
	require.OneOfEquals(t, "data", repoNames)

	cmd := exec.Command(
		"pachctl",
		"put-file",
		"data",
		"master",
		"sales",
		"-c",
		"-f",
		"../../../doc/examples/fruit_stand/set1.txt",
	)
	_, err = cmd.CombinedOutput()
	require.NoError(t, err)

	commitInfos, err := c.ListCommit(
		[]*pfsclient.Commit{{
			Repo: &pfsclient.Repo{"data"},
		}},
		nil,
		client.CommitTypeRead,
		client.CommitStatusNormal,
		false,
	)
	require.Equal(t, 1, len(commitInfos))
	commit := commitInfos[0].Commit

	var buffer bytes.Buffer
	require.NoError(t, c.GetFile("data", commit.ID, "sales", 0, 0, "", false, nil, &buffer))
	lines := strings.Split(buffer.String(), "\n")
	if len(lines) < 100 {
		t.Fatalf("Sales file has too few lines (%v)\n", len(lines))
	}

	cmd = exec.Command(
		"pachctl",
		"create-pipeline",
		"-f",
		"../../../doc/examples/fruit_stand/pipeline.json",
	)
	_, err = cmd.CombinedOutput()
	require.NoError(t, err)

	repoInfos, err = c.ListRepo(nil)
	require.NoError(t, err)
	repoNames = []interface{}{}
	for _, repoInfo := range repoInfos {
		repoNames = append(repoNames, repoInfo.Repo.Name)
	}
	require.OneOfEquals(t, "sum", repoNames)
	require.OneOfEquals(t, "filter", repoNames)
	require.OneOfEquals(t, "data", repoNames)

	commitInfos, err = c.FlushCommit([]*pfsclient.Commit{client.NewCommit("data", commit.ID)}, nil)
	require.NoError(t, err)
	require.Equal(t, 3, len(commitInfos))

	commitInfos, err = c.ListCommit(
		[]*pfsclient.Commit{{
			Repo: &pfsclient.Repo{"sum"},
		}},
		nil,
		client.CommitTypeRead,
		client.CommitStatusNormal,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, 1, len(commitInfos))

	buffer.Reset()
	require.NoError(t, c.GetFile("sum", commitInfos[0].Commit.ID, "apple", 0, 0, "", false, nil, &buffer))
	require.NotNil(t, buffer)
	firstCount, err := strconv.ParseInt(strings.TrimRight(buffer.String(), "\n"), 10, 0)
	require.NoError(t, err)
	if firstCount < 100 {
		t.Fatalf("Wrong sum for apple (%i) ... too low\n", firstCount)
	}

	// Add more data to input

	cmd = exec.Command(
		"pachctl",
		"put-file",
		"data",
		"master",
		"sales",
		"-c",
		"-f",
		"../../../doc/examples/fruit_stand/set2.txt",
	)
	_, err = cmd.Output()
	require.NoError(t, err)

	// Flush commit!
	commitInfos, err = c.ListCommit(
		[]*pfsclient.Commit{{
			Repo: &pfsclient.Repo{"data"},
		}},
		nil,
		client.CommitTypeRead,
		client.CommitStatusAll,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, 2, len(commitInfos))

	commitInfos, err = c.FlushCommit([]*pfsclient.Commit{client.NewCommit("data", commitInfos[1].Commit.ID)}, nil)
	require.NoError(t, err)
	require.Equal(t, 3, len(commitInfos))

	commitInfos, err = c.ListCommit(
		[]*pfsclient.Commit{{
			Repo: &pfsclient.Repo{"sum"},
		}},
		nil,
		client.CommitTypeRead,
		client.CommitStatusNormal,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, 2, len(commitInfos))

	buffer.Reset()
	require.NoError(t, c.GetFile("sum", commitInfos[1].Commit.ID, "apple", 0, 0, "", false, nil, &buffer))
	require.NotNil(t, buffer)
	secondCount, err := strconv.ParseInt(strings.TrimRight(buffer.String(), "\n"), 10, 0)
	require.NoError(t, err)
	if firstCount > secondCount {
		t.Fatalf("Second sum (%v) is smaller than first (%v)\n", secondCount, firstCount)
	}

	require.NoError(t, c.DeleteRepo("sum", false))
	require.NoError(t, c.DeleteRepo("filter", false))
	require.NoError(t, c.DeleteRepo("data", false))
}
