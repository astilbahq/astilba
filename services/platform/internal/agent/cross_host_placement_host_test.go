package agent

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/client"
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

// loadNativeImages copies pinned images from the source daemon into each fixture host.
func loadNativeImages(ctx context.Context, t *testing.T, hosts []*nativePlacementHost, images ...string) {
	t.Helper()
	source, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, image := range images {
		if _, err = source.ImageInspect(ctx, image); err != nil {
			if err = (&Engine{Docker: source}).pullImage(ctx, image); err != nil {
				t.Fatal(err)
			}
		}
		for _, host := range hosts {
			archive, err := source.ImageSave(ctx, []string{image})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := host.docker.ImageLoad(ctx, archive)
			if err != nil {
				archive.Close()
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(loaded)
			loaded.Close()
			archive.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, err = host.docker.ImageInspect(ctx, image); err != nil {
				// Docker's OCI export retains the content digest but drops its
				// repository name. Restore that name without changing the content.
				named, parseErr := reference.ParseNormalizedNamed(image)
				digested, ok := named.(reference.Digested)
				if parseErr != nil || !ok {
					t.Fatal("fixture requires pinned registry images")
				}
				alias, aliasErr := exec.CommandContext(ctx, "/opt/astilba-plat4/bin/ctr", "--address", filepath.Join(host.root, "exec/containerd/containerd.sock"), "--namespace", "moby", "images", "tag", "moby-dangling@"+digested.Digest().String(), named.Name()+"@"+digested.Digest().String()).CombinedOutput()
				if _, err = host.docker.ImageInspect(ctx, image); aliasErr != nil || err != nil {
					t.Fatalf("fixture pinned image %s: %s %s (%v, %v)", image, body, alias, aliasErr, err)
				}
			}
		}
	}
}
