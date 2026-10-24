package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
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
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "owned-builder", "Config": map[string]any{"Image": pipeline.BuilderImage, "Labels": map[string]string{labelServer: c.ServerId.String(), labelResource: c.Id.String(), labelEnvironment: c.EnvironmentId.String(), labelKind: "pipeline-v1", "com.astilba.plan": c.PlanHash}}})
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
	raw, _ := json.Marshal(p)
	message := json.RawMessage(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: plan.ServerID, EnvironmentId: plan.EnvironmentID, ResourceId: plan.UnitID, Kind: "pipeline", Generation: 1, PlanHash: p.Digest(), Pipeline: &message}
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	p.Access, p.BuildSecrets, p.RegistryAccess = nil, nil, nil
	raw, _ = json.Marshal(p)
	message = json.RawMessage(raw)
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
	raw, _ = json.Marshal(p)
	message = json.RawMessage(raw)
	c.Pipeline = &message
	if _, err = j.Accept(c); err == nil {
		t.Fatal("changed frozen source accepted under old plan hash")
	}
}
