package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/catalog"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

func nativeCatalogJourney(t *testing.T, e *Engine, server uuid.UUID, command func(string, deployment.Plan, map[string]string) api.ExecutionCommand) {
	for _, recipe := range catalog.Recipes() {
		t.Run(recipe.ID, func(t *testing.T) {
			ctx := context.Background()
			spec := recipe.Specification
			a := &spec.Applications[0]
			a.ID = uuid.New()
			for i := range spec.Volumes {
				old := spec.Volumes[i].ID
				spec.Volumes[i].ID = uuid.New()
				for j := range a.Mounts {
					if a.Mounts[j].VolumeID == old {
						a.Mounts[j].VolumeID = spec.Volumes[i].ID
					}
				}
			}
			secrets := map[string]string{}
			if a.Postgres != nil {
				a.Postgres.PasswordSecretVersionID = uuid.New()
				secrets[a.Postgres.PasswordSecretVersionID.String()] = "catalog/$secret@canary"
			}
			source := a.ID
			consumer, image := uuid.New(), execution.TestImage
			spec.Applications = append(spec.Applications, domain.Application{ID: consumer, Name: "Consumer", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Command: []string{"sleep", "3600"}})
			for _, output := range []string{"HOST", "PORT", "URL"} {
				spec.Bindings = append(spec.Bindings, domain.Binding{ID: uuid.New(), SourceID: source, TargetID: consumer, Output: output, Variable: "SOURCE_" + output})
			}
			p := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: spec, Images: map[string]string{}, ExpiresAt: time.Now().Add(time.Hour)}.WithHash()
			t.Cleanup(func() {
				filter := client.Filters{}.Add("label", labelServer+"="+server.String(), "com.astilba.unit="+p.UnitID.String())
				containers, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
				if err != nil {
					t.Error(err)
					return
				}
				for _, v := range containers.Items {
					if visualOwned(v.Labels, p) {
						_, err = e.Docker.ContainerRemove(ctx, v.ID, client.ContainerRemoveOptions{Force: true})
						if err != nil {
							t.Error(err)
						}
					}
				}
				volumes, err := e.Docker.VolumeList(ctx, client.VolumeListOptions{Filters: filter})
				if err != nil {
					t.Error(err)
					return
				}
				for _, v := range volumes.Items {
					if visualOwned(v.Labels, p) {
						_, err = e.Docker.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{})
						if err != nil {
							t.Error(err)
						}
					}
				}
				n, err := e.Docker.NetworkInspect(ctx, deployment.NetworkName(p.UnitID), client.NetworkInspectOptions{})
				if err == nil && visualOwned(n.Network.Labels, p) {
					_, err = e.Docker.NetworkRemove(ctx, n.Network.ID, client.NetworkRemoveOptions{})
					if err != nil {
						t.Error(err)
					}
				}
			})
			finish := func(action string, secrets map[string]string) deployment.Observation {
				t.Helper()
				c := command(action, p, secrets)
				if err := e.Execute(ctx, c); err != nil {
					t.Fatal(err)
				}
				r, err := e.Journal.Record(c.Id)
				if err != nil || r.Report.State != "succeeded" {
					t.Fatalf("%s: %v %s", action, err, r.Report.Note)
				}
				if err = e.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
					t.Fatal(err)
				}
				var observation deployment.Observation
				if err = json.Unmarshal(*r.Report.Observation, &observation); err != nil {
					t.Fatal(err)
				}
				return observation
			}
			resolved := finish("resolve", nil)
			p.Images, p.ExpectedState = resolved.Images, resolved.StateHash
			p = p.WithHash()
			finish("deploy", secrets)
			inspect, err := e.Docker.ContainerInspect(ctx, p.ContainerName(source), client.ContainerInspectOptions{})
			if err != nil || !inspect.Container.State.Running || len(inspect.Container.HostConfig.PortBindings) != 0 {
				t.Fatal("recipe failed private deployment", err)
			}
			consumerState, err := e.Docker.ContainerInspect(ctx, p.ContainerName(consumer), client.ContainerInspectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{}
			for _, raw := range consumerState.Container.Config.Env {
				k, v, _ := strings.Cut(raw, "=")
				env[k] = v
			}
			u, err := url.Parse(env["SOURCE_URL"])
			if err != nil || env["SOURCE_HOST"] != p.HostName(source) || u.Hostname() != p.HostName(source) || u.Port() != env["SOURCE_PORT"] {
				t.Fatal("runtime binding differs from private source")
			}
			exec := func(id uuid.UUID, args ...string) string {
				t.Helper()
				call, err := e.Docker.ExecCreate(ctx, p.ContainerName(id), client.ExecCreateOptions{AttachStdout: true, AttachStderr: true, Cmd: args})
				if err != nil {
					t.Fatal(err)
				}
				stream, err := e.Docker.ExecAttach(ctx, call.ID, client.ExecAttachOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				_, err = stdcopy.StdCopy(&output, &output, io.LimitReader(stream.Reader, 65536))
				stream.Close()
				if err != nil {
					t.Fatal(err)
				}
				state, err := e.Docker.ExecInspect(ctx, call.ID, client.ExecInspectOptions{})
				if err != nil || state.ExitCode != 0 {
					t.Fatalf("catalog probe failed: %v %s", err, output.String())
				}
				return output.String()
			}
			waitForCache := func() {
				t.Helper()
				exec(source, "timeout", "15", "sh", "-c", `until [ "$(valkey-cli --raw -h "$1" PING 2>/dev/null)" = PONG ]; do sleep 0.1; done`, "probe", env["SOURCE_HOST"])
			}
			if recipe.ID == "valkey-cache" {
				waitForCache()
				got := exec(source, "valkey-cli", "-h", env["SOURCE_HOST"], "SET", "fixture", "connected")
				if !strings.Contains(got, "OK") || !strings.Contains(exec(source, "valkey-cli", "GET", "fixture"), "connected") {
					t.Fatal("cache roundtrip failed")
				}
			} else if recipe.ID != "postgresql-18" {
				if len(exec(consumer, "wget", "-qO-", env["SOURCE_URL"])) == 0 {
					t.Fatal("consumer could not use the bound HTTP URL")
				}
			}
			// Ordinary Docker restart retains a recipe's existing volume identities.
			if _, err = e.Docker.ContainerRestart(ctx, p.ContainerName(source), client.ContainerRestartOptions{}); err != nil {
				t.Fatal(err)
			}
			if recipe.ID == "valkey-cache" {
				waitForCache()
				if strings.TrimSpace(exec(source, "valkey-cli", "GET", "fixture")) != "" {
					t.Fatal("ephemeral cache retained data")
				}
			}
			finish("observe", secrets)
		})
	}
}
