package autobahn

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mccutchen/websocket/internal/testing/assert"
)

const autobahnImage = "crossbario/autobahn-testsuite:0.8.2"

var defaultIncludedTestCases = []string{
	"*",
}

var defaultExcludedTestCases = []string{
	// Compression extensions are not supported
	"12.*",
	"13.*",
}

var allowNonStrict = map[string]bool{
	// Some weirdness in these test cases, where they expect the server to
	// time out and close the connection, but it's not clear after exactly
	// how long the timeout should happen (and, AFAICT, other test cases
	// expect a different timeout).
	//
	// The cases pass with "NON-STRICT" results when the timeout is not
	// hit, as long as we return the expected 1007 status code.
	"6.4.1": true,
	"6.4.2": true,
	"6.4.3": true,
	"6.4.4": true,
}

type Results struct{}

// Run runs the autobahn fuzzing client test suite against the given target
// URL, optionally limiting to only the specified cases. Autobahn's test
// results will be written to outDir.
func Run(targetURL string, cases []string, outDir string) (Results, error) {

	includedTestCases := defaultIncludedTestCases
	excludedTestCases := defaultExcludedTestCases
	// var hooks websocket.Hooks
	if len(cases) > 0 {
		includedTestCases = cases
		excludedTestCases = []string{}
		// hooks = newTestHooks(t)
	}

	targetURL = newAutobahnTargetURL(targetURL)
	autobahnCfg := map[string]any{
		"servers": []map[string]string{
			{
				"agent": "go-httpbin",
				"url":   targetURL,
			},
		},
		"outdir":        "/testdir/report",
		"cases":         includedTestCases,
		"exclude-cases": excludedTestCases,
	}

	autobahnCfgFile, err := os.Create(path.Join(outDir, "autobahn.json"))
	if err != nil {
		return Results{}, fmt.Errorf("failed to open autobahn config: %w", err)
	}
	if err := json.NewEncoder(autobahnCfgFile).Encode(autobahnCfg); err != nil {
		return Results{}, fmt.Errorf("failed to write autobahn config: %w", err)
	}
	autobahnCfgFile.Close()

	pullCmd := exec.Command("docker", "pull", autobahnImage)
	runCmd(pullCmd)
	testCmd := exec.Command(
		"docker",
		"run",
		"--net=host",
		"--rm",
		"-v", testDir+":/testdir:rw",
		autobahnImage,
		"wstest", "-m", "fuzzingclient", "--spec", "/testdir/autobahn.json",
	)
	runCmd(t, testCmd)

	summary := loadSummary(t, testDir)
	if len(summary) == 0 {
		t.Fatalf("empty autobahn test summary; check autobahn logs for problems connecting to test server at %q", targetURL)
	}

	for _, result := range summary {
		result := result
		t.Run("autobahn/"+result.ID, func(t *testing.T) {
			if result.Failed() {
				report := loadReport(t, testDir, result.ReportFile)
				t.Errorf("description: %s", report.Description)
				t.Errorf("expectation: %s", report.Expectation)
				t.Errorf("want result: %s", report.Result)
				t.Errorf("got result:  %s", report.Behavior)
				t.Errorf("want close:  %s", report.ResultClose)
				t.Errorf("got close:   %s", report.BehaviorClose)
			}
		})
	}

	t.Logf("autobahn test report: file://%s", path.Join(testDir, "report/index.html"))
	if os.Getenv("AUTOBAHN_OPEN_REPORT") != "" {
		runCmd(t, exec.Command("open", path.Join(testDir, "report/index.html")))
	}
}

// newAutobahnTargetURL returns the URL that the autobahn test suite should use
// to connect to the given httptest server.
//
// On Macs, the docker engine is running inside an implicit VM, so even with
// --net=host, we need to use the special hostname to escape the VM.
//
// See the Docker Desktop docs[1] for more information. This same special
// hostname seems to work across Docker Desktop for Mac, OrbStack, and Colima.
//
// [1]: https://docs.docker.com/desktop/networking/#i-want-to-connect-from-a-container-to-a-service-on-the-host
func newAutobahnTargetURL(srvURL string) string {
	u, err := url.Parse(srvURL)
	if err != nil {
		panic("invalid srv URL: " + err.Error())
	}

	var host string
	switch runtime.GOOS {
	case "darwin":
		host = "host.docker.internal"
	default:
		host = "127.0.0.1"
	}

	return fmt.Sprintf("ws://%s:%s%s", host, u.Port(), u.Path)
}

func runCmd(cmd *exec.Cmd) error {
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func newTestDir(t *testing.T) string {
	t.Helper()

	// package tests are run with the package as the working directory, but we
	// want to store our integration test output in the repo root
	testDir, err := filepath.Abs(path.Join(
		"..", "..", ".integrationtests", fmt.Sprintf("autobahn-test-%d", time.Now().Unix()),
	))

	assert.NilError(t, err)
	assert.NilError(t, os.MkdirAll(testDir, 0o755))
	return testDir
}

func loadSummary(t *testing.T, testDir string) []autobahnReportResult {
	t.Helper()
	f, err := os.Open(path.Join(testDir, "report", "index.json"))
	assert.NilError(t, err)
	defer f.Close()
	var summary autobahnReportSummary
	assert.NilError(t, json.NewDecoder(f).Decode(&summary))
	var results []autobahnReportResult
	for _, serverResults := range summary {
		for id, result := range serverResults {
			result.ID = id
			results = append(results, result)
		}
	}
	return results
}

func loadReport(t *testing.T, testDir string, reportFile string) autobahnReportResult {
	t.Helper()
	reportPath := path.Join(testDir, "report", reportFile)
	t.Logf("report data: %s", reportPath)
	t.Logf("report html: file://%s", strings.Replace(reportPath, ".json", ".html", 1))
	f, err := os.Open(reportPath)
	assert.NilError(t, err)
	var report autobahnReportResult
	assert.NilError(t, json.NewDecoder(f).Decode(&report))
	return report
}

type autobahnReportSummary map[string]map[string]autobahnReportResult // server -> case -> result

type autobahnReportResult struct {
	ID            string `json:"id"`
	Behavior      string `json:"behavior"`
	BehaviorClose string `json:"behaviorClose"`
	Description   string `json:"description"`
	Expectation   string `json:"expectation"`
	ReportFile    string `json:"reportfile"`
	Result        string `json:"result"`
	ResultClose   string `json:"resultClose"`
}

func (r autobahnReportResult) Failed() bool {
	okayBehavior := map[string]bool{
		"OK":            true,
		"INFORMATIONAL": true,
	}
	allowNonStrict := map[string]bool{
		// Some weirdness in these test cases, where they expect the server to
		// time out and close the connection, but it's not clear after exactly
		// how long the timeout should happen (and, AFAICT, other test cases
		// expect a different timeout).
		//
		// The cases pass with "NON-STRICT" results when the timeout is not
		// hit, as long as we return the expected 1007 status code.
		"6.4.1": true,
		"6.4.2": true,
		"6.4.3": true,
		"6.4.4": true,
	}
	if allowNonStrict[r.ID] {
		okayBehavior["NON-STRICT"] = true
	}
	return !(okayBehavior[r.Behavior] && okayBehavior[r.BehaviorClose])
}
