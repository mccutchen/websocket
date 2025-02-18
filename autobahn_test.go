package websocket_test

// ============================================================================
// Autobahn Test Suite
// ============================================================================
//
// This test suite runs the 3rd party [Autobahn WebSocket test suite][1]'s
// "fuzzing client" tests against an echo server implemented using this
// websocket package.
//
// Because these tests are slow (60-90s on my laptop) and require a running
// docker daemon, they are disabled by default. To run them, set the AUTOBAHN=1
// environment variable.
//
// By default, the fuzzing client is run against an ephemeral httptest server,
// but it can also be run against an external server by setting the TARGET
// environment variable to the server's URL.
//
// Other customization is possible via the `CASES` and `DEBUG` environment
// variables. See the [README][2] for more info.
//
// [1]: https://github.com/crossbario/autobahn-testsuite
// [2]: https://github.com/mccutchen/websocket/blob/main/README.md#autobahn-integration-tests

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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

func TestAutobahn(t *testing.T) {
	t.Parallel()

	// TODO: document env vars that control test functionality
	if os.Getenv("AUTOBAHN") == "" {
		t.Skipf("set AUTOBAHN=1 to run autobahn integration tests")
	}

	includedTestCases := defaultIncludedTestCases
	excludedTestCases := defaultExcludedTestCases
	if userTestCases := os.Getenv("CASES"); userTestCases != "" {
		t.Logf("using CASES=%q", userTestCases)
		includedTestCases = strings.Split(userTestCases, ",")
		excludedTestCases = []string{}
	}

	// Hooks can be expensive, so only enable them if necessary for debugging
	var hooks websocket.Hooks
	if debug := os.Getenv("DEBUG"); debug == "1" {
		hooks = newTestHooks(t)
	}

	targetURL := os.Getenv("TARGET")
	if targetURL == "" {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, websocket.Options{
				Hooks: hooks,
				// long ReadTimeout because some autobahn test cases (e.g. 5.19)
				// sleep up to 1 second between frames
				ReadTimeout:  5000 * time.Millisecond,
				WriteTimeout: 500 * time.Millisecond,
				// some autobahn test cases send large frames, so we need to
				// support frames and messages up to 16 MiB
				MaxFrameSize:   16 << 20,
				MaxMessageSize: 16 << 20,
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			ws.Serve(r.Context(), websocket.EchoHandler)
		}))
		defer srv.Close()
		targetURL = srv.URL
	} else {
		t.Logf("running autobahn against external target: %s", targetURL)
	}

	testDir := newTestDir(t)
	t.Logf("test dir: %s", testDir)
	targetURL = newAutobahnTargetURL(t, targetURL)
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
	if os.Getenv("REPORT") == "1" {
		runCmd(t, exec.Command("open", path.Join(testDir, "report/index.html")))
	}
}

// newAutobahnTargetURL returns the URL that the autobahn test client should
// use to connect to the given target URL, which may be an ephemeral httptest
// server URL listening on localhost and a random port or some external URL
// provided by the TARGET env var.
//
// Note that the autobahn client will be running inside a container using
// --net=host, so localhost inside the container *should* map to localhost
// outside the container.
//
// On macOS, however, we must use a special "host.docker.internal" hostname for
// localhost addrs, because otherwise localhost inside the container will
// resolve to localhost on the implicit guest VM where docker is running rather
// than localhost on the actual macOS host machine.
//
// See the Docker Desktop docs[1] for more information. This same special
// hostname seems to work across Docker Desktop for Mac, OrbStack, and Colima.
//
// [1]: https://docs.docker.com/desktop/networking/#i-want-to-connect-from-a-container-to-a-service-on-the-host
func newAutobahnTargetURL(t *testing.T, targetURL string) string {
	t.Helper()
	if matched, _ := regexp.MatchString("^https?://", targetURL); !matched {
		targetURL = "http://" + targetURL
	}
	u, err := url.Parse(targetURL)
	assert.NilError(t, err)
	u.Scheme = "ws"
	if runtime.GOOS == "darwin" && isLocalhost(u.Hostname()) {
		host := "host.docker.internal"
		_, port, _ := net.SplitHostPort(u.Host)
		if port != "" {
			host = net.JoinHostPort(host, port)
		}
		u.Host = host
	}
	return u.String()
}

func isLocalhost(ipAddr string) bool {
	ipAddr = strings.ToLower(ipAddr)
	for _, addr := range []string{
		"localhost",
		"127.0.0.1",
		"::1",
		"0:0:0:0:0:0:0:1",
	} {
		if ipAddr == addr {
			return true
		}
	}
	return false
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
	reportDir := os.Getenv("REPORT_DIR")
	if reportDir == "" {
		reportDir = path.Join(
			".integrationtests", fmt.Sprintf("autobahn-test-%d", time.Now().Unix()),
		)
	}
	testDir, err := filepath.Abs(reportDir)
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
