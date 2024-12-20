package websocket_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mccutchen/websocket"
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

func TestWebSocketServer(t *testing.T) {
	t.Parallel()

	if os.Getenv("AUTOBAHN_TESTS") == "" {
		t.Skipf("set AUTOBAHN_TESTS=1 to run autobahn integration tests")
	}

	includedTestCases := defaultIncludedTestCases
	excludedTestCases := defaultExcludedTestCases
	var hooks websocket.Hooks
	if userTestCases := os.Getenv("AUTOBAHN_CASES"); userTestCases != "" {
		t.Logf("using AUTOBAHN_CASES=%q", userTestCases)
		includedTestCases = strings.Split(userTestCases, ",")
		excludedTestCases = []string{}
		hooks = newTestHooks(t)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Options{
			Hooks: hooks,
			// long ReadTimeout because some autobahn test cases (e.g. 5.19
			// sleep up to 1 second between frames)
			ReadTimeout:  5000 * time.Millisecond,
			WriteTimeout: 500 * time.Millisecond,
			// some autobahn test cases send large frames, so we need to
			// support large fragments and messages
			MaxFragmentSize: 1024 * 1024 * 16,
			MaxMessageSize:  1024 * 1024 * 16,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ws.Serve(r.Context(), websocket.EchoHandler)
	}))
	defer srv.Close()

	testDir := newTestDir(t)
	t.Logf("test dir: %s", testDir)

	targetURL := newAutobahnTargetURL(t, srv)
	t.Logf("target url: %s", targetURL)

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

	autobahnCfgFile, err := os.Create(path.Join(testDir, "autobahn.json"))
	assert.NilError(t, err)
	assert.NilError(t, json.NewEncoder(autobahnCfgFile).Encode(autobahnCfg))
	autobahnCfgFile.Close()

	pullCmd := exec.Command("docker", "pull", autobahnImage)
	runCmd(t, pullCmd)

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
func newAutobahnTargetURL(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	assert.NilError(t, err)

	var host string
	switch runtime.GOOS {
	case "darwin":
		host = "host.docker.internal"
	default:
		host = "127.0.0.1"
	}

	return fmt.Sprintf("ws://%s:%s/websocket/echo", host, u.Port())
}

func runCmd(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	t.Logf("running command: %s", cmd.String())
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	assert.NilError(t, cmd.Run())
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
