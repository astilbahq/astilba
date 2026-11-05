package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func TestPipelineCompletionWaitsForOwnedCleanup(t *testing.T) {
	j, plan := testVisualJournal(t)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: plan.ServerID, EnvironmentId: plan.EnvironmentID, ResourceId: plan.UnitID, Kind: "pipeline", Generation: 1, PlanHash: strings.Repeat("a", 64)}
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	fail, removed := true, false
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "DELETE" {
			if fail {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"message":"busy"}`))
				return
			}
			removed = true
			w.WriteHeader(204)
			return
		}
		if removed {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"gone"}`))
			return
		}
		_ = stablejson.Encode(w, map[string]any{"Id": "owned-builder", "Config": map[string]any{"Image": pipeline.BuilderImage, "Labels": map[string]string{labelServer: c.ServerId.String(), labelResource: c.Id.String(), labelEnvironment: c.EnvironmentId.String(), labelKind: "pipeline-v1", "com.astilba.plan": c.PlanHash}}})
	}))
	defer daemon.Close()
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	e := Engine{Docker: docker, Journal: j}
	if err = e.finishPipeline(c, "succeeded", "complete"); err != nil {
		t.Fatal(err)
	}
	r, _ := j.Record(c.Id)
	if r.Report.State != "needs_attention" || removed {
		t.Fatal("cleanup failure was reported as completion")
	}
	fail = false
	if err = e.finishPipeline(c, "succeeded", "complete"); err != nil {
		t.Fatal(err)
	}
	r, _ = j.Record(c.Id)
	if r.Report.State != "succeeded" || !removed {
		t.Fatal("successful completion preceded container removal")
	}
	if err = e.removePipeline(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineJournalAcceptsCredentialFreeCancellationAndRejectsChangedInputs(t *testing.T) {
	j, plan := testVisualJournal(t)
	p := pipeline.Command{Action: "build", Commit: strings.Repeat("a", 40), Branch: "main", RevisionID: uuid.New(), UnitID: plan.UnitID, AppID: uuid.New(), InputHash: strings.Repeat("b", 64), Access: &pipeline.Access{Clone: pipeline.Credential{Password: "clone-canary"}}, BuildSecrets: map[string]string{"token": "build-canary"}, RegistryAccess: &pipeline.RegistryAccess{Push: pipeline.Credential{Password: "push-canary"}}}
	raw, _ := stablejson.Marshal(p)
	message := jsontext.Value(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: plan.ServerID, EnvironmentId: plan.EnvironmentID, ResourceId: plan.UnitID, Kind: "pipeline", Generation: 1, PlanHash: p.Digest(), Pipeline: &message}
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	p.Access, p.BuildSecrets, p.RegistryAccess = nil, nil, nil
	raw, _ = stablejson.Marshal(p)
	message = jsontext.Value(raw)
	c.Pipeline = &message
	c.CancelRequested = true
	record, err := j.Accept(c)
	if err != nil || !record.Command.CancelRequested {
		t.Fatal("credential-free cancellation was not latched", err)
	}
	var retained pipeline.Command
	_ = json.Unmarshal(*record.Command.Pipeline, &retained)
	if retained.Access == nil || retained.Access.Clone.Password != "clone-canary" {
		t.Fatal("cancellation replaced frozen execution credentials")
	}
	p.Commit = strings.Repeat("c", 40)
	raw, _ = stablejson.Marshal(p)
	message = jsontext.Value(raw)
	c.Pipeline = &message
	if _, err = j.Accept(c); err == nil {
		t.Fatal("changed frozen source accepted under old plan hash")
	}
}

func TestPipelineCombinedLogsRemainReportable(t *testing.T) {
	journal, plan := testVisualJournal(t)
	inputs := pipeline.RailpackFor(runtime.GOARCH)
	connection := pipeline.Connection{ID: uuid.New(), Provider: "github", BaseURL: "https://github.com", Repository: "example/app"}
	p := pipeline.Command{
		Action: "build", Connection: connection, Commit: strings.Repeat("a", 40), Branch: "main",
		RevisionID: uuid.New(), UnitID: plan.UnitID, AppID: uuid.New(), InputHash: strings.Repeat("b", 64),
		Access: &pipeline.Access{}, Railpack: &inputs,
		Build: &pipeline.BuildConfig{
			Builder: "railpack", ConnectionID: connection.ID, RunnerID: plan.ServerID, Branch: "main",
			Architecture: runtime.GOARCH, Context: ".", Railpack: &pipeline.RailpackOptions{},
			Budget: &pipeline.BuildBudget{CPUs: 2, MemoryMiB: 6144, SourceMiB: 1024, StateMiB: 4096, Minutes: 30},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := stablejson.Marshal(p)
	message := jsontext.Value(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: plan.ServerID, EnvironmentId: plan.EnvironmentID, ResourceId: plan.UnitID, Kind: "pipeline", Generation: 1, PlanHash: p.Digest(), Pipeline: &message, Deadline: time.Now().Add(time.Hour)}
	if _, err := journal.Accept(c); err != nil {
		t.Fatal(err)
	}

	var planner bytes.Buffer
	archive := tar.NewWriter(&planner)
	if err := archive.WriteHeader(&tar.Header{Name: "railpack", Typeflag: tar.TypeReg, Mode: 0500, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	pathStat := base64.StdEncoding.EncodeToString([]byte(`{"name":"railpack","size":1,"mode":320}`))
	labels := map[string]string{labelServer: c.ServerId.String(), labelResource: c.Id.String(), labelEnvironment: c.EnvironmentId.String(), labelKind: "pipeline-planner-v1", "com.astilba.plan": c.PlanHash}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/containers/create"):
			_, _ = w.Write([]byte(`{"Id":"owned-planner"}`))
		case strings.HasSuffix(r.URL.Path, "/archive"):
			w.Header().Set("X-Docker-Container-Path-Stat", pathStat)
			_, _ = w.Write(planner.Bytes())
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			_ = stablejson.Encode(w, map[string]any{"Id": "owned-planner", "Config": map[string]any{"Image": inputs.Frontend, "Labels": labels}})
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer daemon.Close()
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.56"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()

	dir := t.TempDir()
	files := map[string]string{
		"planner.log": strings.Repeat("é", 8192),
		"plan.json":   `{"steps":[{"name":"build","commands":[{"cmd":"echo built"}]}],"deploy":{"base":{"image":"scratch"},"startCommand":"sh"}}`,
		"info.json":   `{"success":true,"railpackVersion":"` + inputs.Version + `","detectedProviders":["shell"],"resolvedPackages":{}}`,
		"docker": `#!/bin/sh
case "$*" in
 *"/workspace/planner/railpack prepare "*) cat "$(dirname "$0")/planner.log";;
 *"cat /workspace/planner/railpack-plan.json"*) cat "$(dirname "$0")/plan.json";;
 *"cat /workspace/planner/info.json"*) cat "$(dirname "$0")/info.json";;
 *" /workspace/context/railpack.json "*) printf '{}';;
 *"buildctl build "*) printf x; exit 7;;
 *) cat >/dev/null;;
esac
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	engine := Engine{Journal: journal, Docker: docker, DockerCLI: filepath.Join(dir, "docker")}
	result, buildErr := engine.buildPipeline(t.Context(), c, p)
	if buildErr == nil {
		t.Fatal("fixture build unexpectedly succeeded")
	}
	encoded, err := stablejson.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded pipeline.Result
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Logs) > pipeline.LogLimit || decoded.Logs != strings.Repeat("é", 8191)+"x" {
		t.Fatalf("combined phase logs are not reportable: bytes=%d, build=%v", len(decoded.Logs), buildErr)
	}
}
