package agent

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
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

// nativeCatalogJourney stages every catalog entry as the API does and deploys it privately, with
// a consumer of its first service's outputs. An entry with a route publishes it on this host's
// ingress, which must refuse a client outside the staged allowlist.
func nativeCatalogJourney(t *testing.T, e *Engine, server uuid.UUID, command func(string, deployment.Plan, map[string]string) api.ExecutionCommand, requests *http.Client) {
	for _, recipe := range catalog.Recipes() {
		t.Run(recipe.ID, func(t *testing.T) {
			ctx := context.Background()
			in := catalog.Stage{Name: recipe.Name}
			host := recipe.ID + ".catalog.example.test"
			if recipe.Route != nil {
				// Not this host's address, so the ingress must turn the request away.
				in.Route = &catalog.StageRoute{Domain: host, TLS: "off", AllowCIDRs: []string{"203.0.113.7/32"}}
			}
			staged, err := recipe.Stage(in, nil)
			if err != nil {
				t.Fatal(err)
			}
			spec := staged.Specification
			secrets := map[string]string{}
			for _, g := range staged.Generated {
				if secrets[g.VersionID.String()], err = g.Value(); err != nil {
					t.Fatal(err)
				}
			}
			// A database password that needs URL encoding, to check each consumer decodes it.
			for _, a := range spec.Applications {
				if a.Postgres != nil {
					secrets[a.Postgres.PasswordSecretVersionID.String()] = "catalog/$secret@canary"
				}
			}
			source := spec.Applications[0].ID
			consumer, image := uuid.New(), execution.TestImage
			spec.Applications = append(spec.Applications, domain.Application{ID: consumer, Name: "Consumer", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Command: []string{"sleep", "3600"}})
			for _, output := range []string{"HOST", "PORT", "URL"} {
				spec.Bindings = append(spec.Bindings, domain.Binding{ID: uuid.New(), SourceID: source, TargetID: consumer, Output: output, Variable: "SOURCE_" + output})
			}
			p := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: spec, Images: map[string]string{}, HostChanges: len(spec.Routes) > 0, ExpiresAt: time.Now().Add(time.Hour)}.WithHash()
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
			// Uptime Kuma runs as non-root. Readiness already shows it could write its new
			// volume, since it creates directories there before it listens.
			if recipe.ID == "uptime-kuma" {
				exec(source, "sh", "-c", `[ "$(id -u)" != 0 ]`)
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
			if recipe.Route == nil {
				return
			}
			get := func(path string) int {
				t.Helper()
				req, _ := http.NewRequest("GET", "http://127.0.0.1"+path, nil)
				req.Host = host
				res, err := requests.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				return res.StatusCode
			}
			if code := get("/"); code != http.StatusForbidden {
				t.Fatal("route admitted a client outside its allowlist", code)
			}
			// Opening the route to the owner's network is a later, reviewed change.
			redeploy := func(next deployment.Plan) {
				t.Helper()
				next.ID, next.RevisionID, next.Images = uuid.New(), uuid.New(), map[string]string{}
				p = next.WithHash()
				resolved := finish("resolve", nil)
				p.Images, p.ExpectedState = resolved.Images, resolved.StateHash
				p = p.WithHash()
				finish("deploy", secrets)
			}
			opened := p
			opened.Specification.Routes = []domain.Route{spec.Routes[0]}
			opened.Specification.Routes[0].AllowCIDRs = []string{"127.0.0.1/32"}
			redeploy(opened)
			// The application may still be migrating its database behind its readiness check.
			health := "/"
			for _, a := range spec.Applications {
				if a.ID == spec.Routes[0].TargetID && a.Health != nil {
					health = a.Health.Path
				}
			}
			for deadline := time.Now().Add(3 * time.Minute); get(health) != http.StatusOK; time.Sleep(time.Second) {
				if time.Now().After(deadline) {
					t.Fatal("route did not reach the application's readiness")
				}
			}
			if recipe.ID == "kaneo" {
				// Kaneo reached its database through the bound URL, whose password needs
				// encoding, and created its schema there.
				database := spec.Applications[1].ID
				tables := exec(database, "psql", "-U", "kaneo", "-d", "kaneo", "-tAc", "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'")
				if n := strings.TrimSpace(tables); n == "" || n == "0" {
					t.Fatal("Kaneo created no tables in its database")
				}
			}
			// An empty plan removes the entry's services and route from this host's ingress.
			empty := p
			empty.Specification = domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}}
			redeploy(empty)
			if code := get("/"); code == http.StatusOK {
				t.Fatal("removed route still answers")
			}
		})
	}
}
