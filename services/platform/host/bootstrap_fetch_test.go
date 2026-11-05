package host

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// Runs the served script's own download function against a local HTTPS server, with its
// waits recorded instead of slept, so what it retries and what it reports cannot drift from
// what ships.
func TestBootstrapFetchSurvivesOutagesAndNamesTheURL(t *testing.T) {
	script := string(Bootstrap())
	die := regexp.MustCompile(`(?m)^die\(\) .*$`).FindString(script)
	fetch := regexp.MustCompile(`(?ms)^fetch\(\) \{.*?^\}$`).FindString(script)
	if die == "" || fetch == "" {
		t.Fatal("the bootstrap no longer defines die and fetch")
	}
	const body = "pinned file"
	gatewayTimeout := func(w http.ResponseWriter) { w.WriteHeader(http.StatusGatewayTimeout) }
	dropConnection := func(http.ResponseWriter) { panic(http.ErrAbortHandler) }
	for _, test := range []struct {
		name     string
		failures []func(http.ResponseWriter)
		always   func(http.ResponseWriter)
		requests int64 // Zero when the count is a detail of the schedule.
		minWait  int
		ok       bool
		output   string
	}{
		{name: "an outage ends", failures: []func(http.ResponseWriter){gatewayTimeout, gatewayTimeout, gatewayTimeout, gatewayTimeout}, requests: 5, ok: true, output: "HTTP 504"},
		{name: "a stream is dropped", failures: []func(http.ResponseWriter){dropConnection}, requests: 2, ok: true, output: "failed (curl exit"},
		{name: "rate limited", failures: []func(http.ResponseWriter){func(w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) }}, requests: 2, ok: true, output: "HTTP 429"},
		{name: "an outage outlasts the attempts", always: gatewayTimeout, minWait: 240, output: "Could not download"},
		{name: "a refusal will not change", always: func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) }, requests: 1, output: "refused the download with HTTP 404"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := int(requests.Add(1))
				switch {
				case test.always != nil:
					test.always(w)
				case n <= len(test.failures):
					test.failures[n-1](w)
				default:
					_, _ = w.Write([]byte(body))
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			ca := filepath.Join(dir, "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "file")
			url := server.URL + "/asset"
			slept := filepath.Join(dir, "slept")
			harness := "set -euo pipefail\nsleep() { echo \"$1\" >> \"$SLEPT\"; }\n" + die + "\n" + fetch + "\nfetch \"$1\" \"$2\" 1024\n"
			command := exec.Command("bash", "-c", harness, "bash", url, target)
			command.Env = append(os.Environ(), "CURL_CA_BUNDLE="+ca, "SLEPT="+slept)
			out, err := command.CombinedOutput()
			if (err == nil) != test.ok {
				t.Fatalf("exit %v: %s", err, out)
			}
			if got := requests.Load(); test.requests != 0 && got != test.requests {
				t.Fatalf("made %d requests, want %d: %s", got, test.requests, out)
			}
			delays, _ := os.ReadFile(slept)
			waited := 0
			for _, delay := range strings.Fields(string(delays)) {
				seconds, err := strconv.Atoi(delay)
				if err != nil {
					t.Fatal(err)
				}
				waited += seconds
			}
			if waited < test.minWait {
				t.Fatalf("waited %d s, want at least %d: %s", waited, test.minWait, out)
			}
			if !strings.Contains(string(out), url) || !strings.Contains(string(out), test.output) {
				t.Fatalf("output does not name %s and %q: %s", url, test.output, out)
			}
			if test.ok {
				if data, err := os.ReadFile(target); err != nil || string(data) != body {
					t.Fatalf("downloaded %q, %v", data, err)
				}
			}
		})
	}
}
