package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// templateSnapshot holds a neighbouring environment and a two-replica application
// whose stored health path is the given value.
func templateSnapshot(path string) routeSnapshot {
	neighbour, unit := uuid.MustParse("11111111-1111-4111-8111-111111111111"), uuid.MustParse("99999999-9999-4999-8999-999999999999")
	neighbourApp, app := uuid.MustParse("33333333-3333-4333-8333-333333333333"), uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	return routeSnapshot{
		Revision: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		Ports:    []int{80, 443},
		Sets: map[string]deployment.RouteSet{
			neighbour.String(): {UnitID: neighbour, EnvironmentID: neighbour, Routes: []domain.Route{{ID: uuid.MustParse("22222222-2222-4222-8222-222222222222"), TargetID: neighbourApp, Protocol: "http", Domain: "neighbour.example.test", Port: 80, TLS: "off", AllowCIDRs: []string{"10.0.0.0/8"}}}, Backends: map[string]deployment.Backend{neighbourApp.String(): {Address: deployment.ServiceName(neighbourApp)}}},
			unit.String():      {UnitID: unit, EnvironmentID: unit, Routes: []domain.Route{{ID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), TargetID: app, Protocol: "http", Domain: "replicas.example.test", Port: 8080, TLS: "off"}}, Backends: map[string]deployment.Backend{app.String(): {InstanceIDs: []uuid.UUID{app, uuid.MustParse("cccccccc-cccc-4ccc-8ccc-cccccccccccc")}, Health: &domain.HealthCheck{Port: 8080, Path: path}}}},
		},
	}
}

// A host can hold a path stored before such paths were refused. It must keep rendering,
// or the host could not report on or change the routes of any environment.
func TestStoredHealthPathRendersInert(t *testing.T) {
	const path = "/{{x}}"
	raw, err := renderSnapshot(templateSnapshot(path))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "{{") {
		t.Fatalf("rendered a template action: %s", raw)
	}
	var file struct {
		HTTP struct {
			Routers  map[string]any `json:"routers"`
			Services map[string]struct {
				LoadBalancer struct {
					HealthCheck struct {
						Path string `json:"path"`
					} `json:"healthCheck"`
				} `json:"loadBalancer"`
			} `json:"services"`
		} `json:"http"`
	}
	if err = json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if got := file.HTTP.Services["astilba-99999999-9999-4999-8999-999999999999-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"].LoadBalancer.HealthCheck.Path; got != path {
		t.Fatalf("health path is %q", got)
	}
	if _, ok := file.HTTP.Routers["activation-eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"]; !ok {
		t.Fatal("activation router is missing")
	}
}

// The file provider evaluates a route file as a Go template and has no setting to
// stop it, so only the pinned proxy can show that a rendered file holds no action.
// A health path that reaches a host must stay one string in one service, however it
// got there, and must not add configuration outside that environment's routes.
func TestPinnedProxyReadsHealthPathsAsPlainStrings(t *testing.T) {
	if testing.Short() {
		t.Skip("real Docker proxy")
	}
	host := os.Getenv("PLAT_APP_DOCKER_HOST")
	if host == "" {
		t.Fatal("PLAT_APP_DOCKER_HOST must explicitly select a disposable daemon")
	}
	docker, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	if _, err = docker.ImageInspect(t.Context(), deployment.ProxyImage); errdefs.IsNotFound(err) {
		pull, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
		defer cancel()
		err = (&Engine{Docker: docker}).pullImage(pull, deployment.ProxyImage)
	}
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"evaluated action": "/h{{printf `%c` 65}}",
		"plain path":       "/ready",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			snapshot := templateSnapshot(path)
			raw, err := renderSnapshot(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			runtime := loadRouteFile(t, docker, snapshot, raw)
			if got := fileRouters(runtime); !slices.Equal(got, []string{"activation-eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee@file", "astilba-11111111-1111-4111-8111-111111111111-22222222-2222-4222-8222-222222222222@file", "astilba-99999999-9999-4999-8999-999999999999-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa@file"}) {
				t.Fatalf("routers outside the reviewed routes: %v", got)
			}
			if len(runtime.TCPRouters) != 0 || len(runtime.TCPServices) != 0 {
				t.Fatalf("configuration outside the reviewed routes: %v %v", runtime.TCPRouters, runtime.TCPServices)
			}
			service := runtime.Services["astilba-99999999-9999-4999-8999-999999999999-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa@file"]
			if service.LoadBalancer.HealthCheck.Path != path {
				t.Fatalf("health path reached the proxy as %q, want %q", service.LoadBalancer.HealthCheck.Path, path)
			}
		})
	}
}

// Traefik adds internal routers of its own, under other provider names.
func fileRouters(runtime proxyRuntime) []string {
	names := []string{}
	for _, name := range slices.Sorted(maps.Keys(runtime.Routers)) {
		if strings.HasSuffix(name, "@file") {
			names = append(names, name)
		}
	}
	return names
}

type proxyRuntime struct {
	Routers     map[string]proxyStatus `json:"routers"`
	TCPRouters  map[string]proxyStatus `json:"tcpRouters"`
	TCPServices map[string]proxyStatus `json:"tcpServices"`
	Services    map[string]struct {
		Status       string `json:"status"`
		LoadBalancer struct {
			HealthCheck struct {
				Path string `json:"path"`
			} `json:"healthCheck"`
		} `json:"loadBalancer"`
	} `json:"services"`
}
type proxyStatus struct {
	Status string `json:"status"`
}

// loadRouteFile starts the pinned proxy with the production arguments and no network,
// and returns what it loaded once it reports every reviewed route enabled.
func loadRouteFile(t *testing.T, docker *client.Client, snapshot routeSnapshot, raw []byte) proxyRuntime {
	t.Helper()
	_, runtime := startRouteProxy(t, docker, snapshot, raw, "none")
	return runtime
}

// startRouteProxy joins the proxy to its first network and connects the rest before it
// starts, so it resolves names through all of them as the shared proxy does.
func startRouteProxy(t *testing.T, docker *client.Client, snapshot routeSnapshot, raw []byte, networks ...string) (string, proxyRuntime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	const ownerLabel = "com.astilba.route-file-fixture"
	owner := uuid.NewString()
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       "astilba-route-file-" + owner,
		Config:     &container.Config{Image: deployment.ProxyImage, Cmd: proxyArgs(snapshot), Labels: map[string]string{ownerLabel: owner}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(networks[0])},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		v, err := docker.ContainerInspect(cleanup, created.ID, client.ContainerInspectOptions{})
		if err != nil || v.Container.Config.Labels[ownerLabel] != owner {
			t.Errorf("proxy cleanup ownership check: %v", err)
			return
		}
		if _, err = docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Error(err)
		}
	})
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	if err = w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "etc/astilba/", Mode: 0o755}); err == nil {
		err = w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "etc/astilba/runtime.yaml", Mode: 0o644, Size: int64(len(raw))})
	}
	if err == nil {
		_, err = w.Write(raw)
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = docker.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: &archive}); err != nil {
		t.Fatal(err)
	}
	for _, name := range networks[1:] {
		if _, err = docker.NetworkConnect(ctx, name, client.NetworkConnectOptions{Container: created.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	for {
		runtime, err := readProxyRuntime(ctx, docker, created.ID)
		if err == nil {
			err = reviewedRoutesEnabled(snapshot, runtime)
		}
		if err == nil {
			return created.ID, runtime
		}
		select {
		case <-ctx.Done():
			t.Fatalf("proxy did not load the route file: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func reviewedRoutesEnabled(s routeSnapshot, runtime proxyRuntime) error {
	if runtime.Routers["activation-"+s.Revision+"@file"].Status != "enabled" {
		return errors.New("activation router is not enabled")
	}
	for _, set := range s.Sets {
		for _, r := range set.Routes {
			name := deployment.UnitName(set.UnitID) + "-" + r.ID.String() + "@file"
			if runtime.Routers[name].Status != "enabled" || runtime.Services[name].Status != "enabled" {
				return fmt.Errorf("route %s is not enabled", name)
			}
		}
	}
	return nil
}

func readProxyRuntime(ctx context.Context, docker *client.Client, id string) (proxyRuntime, error) {
	var runtime proxyRuntime
	exec, err := docker.ExecCreate(ctx, id, client.ExecCreateOptions{AttachStdout: true, AttachStderr: true, Cmd: []string{"wget", "-T", "2", "-qO-", "http://127.0.0.1:8080/api/rawdata"}})
	if err != nil {
		return runtime, err
	}
	attached, err := docker.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
	if err != nil {
		return runtime, err
	}
	defer attached.Close()
	var output bytes.Buffer
	if _, err = stdcopy.StdCopy(&output, io.Discard, attached.Reader); err != nil {
		return runtime, err
	}
	return runtime, json.Unmarshal(output.Bytes(), &runtime)
}
