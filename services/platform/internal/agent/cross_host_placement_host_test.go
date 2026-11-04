package agent

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func nativePlacementRequest(t *testing.T, address, domain string) (int, string) {
	t.Helper()
	status, body, err := nativePlacementAttempt(address, domain)
	if err != nil {
		t.Fatal(err)
	}
	return status, body
}

func nativePlacementAttempt(address, domain string) (int, string, error) {
	request, err := http.NewRequest("GET", "http://"+address+"/cgi-bin/health", nil)
	if err != nil {
		return 0, "", err
	}
	request.Host = domain
	client := http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil {
		return 0, "", err
	}
	return response.StatusCode, string(body), nil
}

// Public health checks run every five seconds independently of local readiness.
// Writer maintenance promises bounded convergence rather than instant routing, so
// a request that outlasts its client timeout is as unrecovered as one answered 503.
// The proxy itself stays up throughout, so any other transport error is a failure.
func nativeWaitPlacementRecovery(t *testing.T) {
	t.Helper()
	started := time.Now()
	for {
		status, body, err := nativePlacementAttempt("10.219.0.1", "cross-host.example.test")
		if err != nil && !os.IsTimeout(err) {
			t.Fatal(err)
		}
		if err == nil && status == 200 {
			t.Logf("public routing recovered after %s", time.Since(started).Round(time.Millisecond))
			return
		}
		if time.Since(started) > 30*time.Second {
			t.Fatalf("application did not recover after writer restart: %d %s %v", status, body, err)
		}
		if err != nil {
			t.Logf("public routing not recovered after %s: %v", time.Since(started).Round(time.Millisecond), err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
