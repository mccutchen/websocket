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

// Run runs the autobahn fuzzing client test suite against the given target
// URL, optionally limiting to only the specified cases. Autobahn's test
// results will be written to outDir.
func Run(targetURL string, cases []string, outDir string) (Report, error) {
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
		return Report{}, fmt.Errorf("failed to open autobahn config: %w", err)
	}
	if err := json.NewEncoder(autobahnCfgFile).Encode(autobahnCfg); err != nil {
		return Report{}, fmt.Errorf("failed to write autobahn config: %w", err)
	}
	autobahnCfgFile.Close()

	pullCmd := exec.Command("docker", "pull", autobahnImage)
	if err := runCmd(pullCmd); err != nil {
		return Report{}, fmt.Errorf("failed to pull docker image: %w", err)
	}

	testCmd := exec.Command(
		"docker",
		"run",
		"--net=host",
		"--rm",
		"-v", outDir+":/testdir:rw",
		autobahnImage,
		"wstest", "-m", "fuzzingclient", "--spec", "/testdir/autobahn.json",
	)
	if err := runCmd(testCmd); err != nil {
		return Report{}, fmt.Errorf("error running autobahn container: %w", err)
	}

	return loadReport(outDir)
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

func loadReport(outDir string) (Report, error) {
	report := Report{
		Path: path.Join(outDir, "report", "index.html"),
	}
	// skip parsing index.json by just loading each test report file we find
	// in the test directory, each named like {name}_case_{n}_{n}_{n}.json
	testResultPaths, _ := filepath.Glob(path.Join(outDir, "report", "*[0-9].json"))
	for _, resultPath := range testResultPaths {
		result, err := loadTestResult(resultPath)
		if err != nil {
			return Report{}, fmt.Errorf("failed to load test result: %w", err)
		}
		report.Results = append(report.Results, result)
	}

	return report, nil
}

func loadTestResult(resultPath string) (TestResult, error) {
	f, err := os.Open(resultPath)
	if err != nil {
		return TestResult{}, fmt.Errorf("failed to open result file: %w", err)
	}
	defer f.Close()
	var result TestResult
	if err := json.NewDecoder(f).Decode(&result); err != nil {
		return TestResult{}, fmt.Errorf("failed to decode test result: %w", err)
	}
	result.ReportFile = resultPath
	return result, err
}

type Report struct {
	Path    string
	Results []TestResult
}

func (r Report) Failed() bool {
	for _, result := range r.Results {
		if result.Failed() {
			return true
		}
	}
	return false
}

type TestResult struct {
	ID            string `json:"id"`
	Behavior      string `json:"behavior"`
	BehaviorClose string `json:"behaviorClose"`
	Description   string `json:"description"`
	Expectation   string `json:"expectation"`
	ReportFile    string `json:"reportfile"`
	Result        string `json:"result"`
	ResultClose   string `json:"resultClose"`
}

func (r TestResult) Failed() bool {
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
