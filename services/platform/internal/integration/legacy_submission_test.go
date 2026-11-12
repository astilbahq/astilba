package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A runner submission queued by an earlier release never receives a credential: one not yet
// delivered fails on the controller, one a runner may hold is cancelled through it, and a success
// that runner reports anyway counts for nothing.
func TestLegacyRunnerSubmissionReceivesNoCredential(t *testing.T) {
	i := newImportedEnvironment(t, "gitea")
	ctx := context.Background()
	// legacy rewrites a queued import as the runner submission an earlier release would have frozen.
	legacy := func(t *testing.T, id uuid.UUID) {
		t.Helper()
		var raw []byte
		if err := i.f.st.Pool.QueryRow(ctx, "SELECT command FROM execution_operations WHERE id=$1", id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var c api.ExecutionCommand
		var p pipeline.Command
		if json.Unmarshal(raw, &c) != nil || c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil {
			t.Fatal("not a pipeline command")
		}
		p.Action, p.SubmissionID = "submit", uuid.New()
		p.Bundle = &composeproject.Bundle{Namespace: p.UnitID, Entrypoints: p.Source.Entrypoints, Files: importHonest}
		payload, _ := stablejson.Marshal(p)
		message := jsontext.Value(payload)
		c.Pipeline, c.PlanHash = &message, p.Digest()
		command, _ := stablejson.Marshal(c)
		if _, err := i.f.st.Pool.Exec(ctx, "UPDATE execution_operations SET command=$2 WHERE id=$1", id, command); err != nil {
			t.Fatal(err)
		}
		if _, err := i.f.st.Pool.Exec(ctx, "UPDATE pipeline_runs SET configuration=$2 WHERE id=$1", id, payload); err != nil {
			t.Fatal(err)
		}
	}
	stopped := func(note string) bool { return strings.Contains(note, "submitted by management") }

	t.Run("never delivered", func(t *testing.T) {
		var op api.Operation
		i.b.call("POST", i.path+"/source/refresh", map[string]any{"baseRevisionId": i.draft.Id}, 202, &op)
		legacy(t, op.Id)
		awaitDispatch(t, ctx, i.f, op.Id)
		var polled api.AgentPoll
		agentRequest(t, i.f.server.Client(), i.f.server.URL, "/agent/v1/poll", i.runner.token, nil, 200, &polled)
		if polled.Command != nil {
			t.Fatal("a runner received a submission", polled.Command.Id)
		}
		if got := i.operation(t, op.Id); got.State != "failed" || !stopped(got.Note) {
			t.Fatal("the submission was not ended on the controller", got.State, got.Note)
		}
	})

	t.Run("possibly delivered", func(t *testing.T) {
		delivered := i.refresh(t)
		legacy(t, delivered.Id)
		again := i.runner.next()
		var frozen pipeline.Command
		if again.Id != delivered.Id || !again.CancelRequested || json.Unmarshal(*again.Pipeline, &frozen) != nil || frozen.Access != nil {
			t.Fatal("a runner that may hold a submission was not stopped without a credential")
		}
		i.runner.finishPipeline(again, "succeeded", pipeline.Result{Commit: strings.Repeat("d", 40), ReviewNumber: 7, ReviewURL: "https://elsewhere.example/review/7"})
		got := i.operation(t, delivered.Id)
		if got.State != "cancelled" {
			t.Fatal("a runner's submission success was accepted", got.State, got.Note)
		}
		var result []byte
		if err := i.f.st.Pool.QueryRow(ctx, "SELECT COALESCE(result::text,'') FROM pipeline_runs WHERE id=$1", delivered.Id).Scan(&result); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(result), "elsewhere.example") {
			t.Fatal("a runner's review link was kept")
		}
	})
}
