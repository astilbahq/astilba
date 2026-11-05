package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A saved draft is frozen at admission, so a value can become a runtime secret
// while its submission queues. The check at delivery decides whether the runner
// receives write credentials for those files.
func TestSubmitDeliveryRechecksRuntimeSecrets(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	head := strings.Repeat("a", 40)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"sha": head}})
			return
		}
		_ = stablejson.Encode(w, map[string]any{"id": 12, "private": true})
	}))
	defer provider.Close()
	const writeCredential = "submit-write-canary"
	var connection struct {
		Connection pipeline.Connection `json:"connection"`
	}
	b.call("POST", prefix+"/git-connections", map[string]any{"name": "Source", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app", "api": map[string]string{"username": "writer", "password": writeCredential}, "clone": map[string]string{"username": "reader", "password": "submit-read-canary"}}, 201, &connection)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Submissions"}, 201, &project)
	enrolled, in, token := enrollment(t, f, b, ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	heartbeat := func(t *testing.T) {
		t.Helper()
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	}
	poll := func(t *testing.T) *api.ExecutionCommand {
		t.Helper()
		heartbeat(t)
		var out api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &out)
		return out.Command
	}
	report := func(t *testing.T, c *api.ExecutionCommand, sequence int64, state api.ExecutionState, note string, result *pipeline.Result) {
		t.Helper()
		r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: sequence, State: state, Note: note, Steps: []api.ExecutionStep{}}
		if result != nil {
			raw, err := stablejson.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			message := jsontext.Value(raw)
			r.Pipeline = &message
		}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, r, 204, nil)
	}
	inContent := func(canary string) func(*composeproject.Bundle) {
		return func(bundle *composeproject.Bundle) { bundle.Files[0].Content += "# " + canary + "\n" }
	}
	// The runner adds this path to the tree it pushes, whatever the file holds.
	inFileName := func(canary string) func(*composeproject.Bundle) {
		return func(bundle *composeproject.Bundle) {
			bundle.Files = append(bundle.Files, composeproject.File{Path: "config/" + canary + ".env", Content: "MODE=plain\n"})
		}
	}
	// prepare leaves an imported environment with plant's change in its saved draft.
	prepare := func(t *testing.T, name string, plant func(*composeproject.Bundle), baseline ...composeproject.File) (string, api.Revision) {
		t.Helper()
		b := *b
		b.t = t
		var env api.Environment
		b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": name, "configurationOwner": "compose"}, 201, &env)
		path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
		source := pipeline.Source{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Paths: []string{"compose.yaml"}, Entrypoints: []string{"compose.yaml"}}
		for _, file := range baseline {
			source.Paths = append(source.Paths, file.Path)
		}
		var imported api.Operation
		b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": source}, 202, &imported)
		heartbeat(t)
		awaitDispatch(t, ctx, f, imported.Id)
		command := poll(t)
		if command == nil || command.Id != imported.Id {
			t.Fatal("import did not reach the runner", command)
		}
		original := "services:\n  web:\n    image: " + pipelineTestImage + "\n"
		report(t, command, 2, "succeeded", "", &pipeline.Result{Commit: head, Files: append([]composeproject.File{{Path: "compose.yaml", Content: original}}, baseline...)})
		var draft api.Revision
		b.call("GET", path+"/draft", nil, 200, &draft)
		var spec domain.Specification
		if err := json.Unmarshal(draft.Specification, &spec); err != nil {
			t.Fatal(err)
		}
		plant(spec.Compose)
		b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
		return path, draft
	}
	submit := func(t *testing.T, path string, draft api.Revision) api.Operation {
		t.Helper()
		b := *b
		b.t = t
		var op api.Operation
		b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": draft.Id, "requestKey": uuid.New()}, 202, &op)
		return op
	}
	dispatched := func(t *testing.T, path string, draft api.Revision) api.Operation {
		t.Helper()
		op := submit(t, path, draft)
		heartbeat(t)
		awaitDispatch(t, ctx, f, op.Id)
		return op
	}
	operation := func(t *testing.T, id uuid.UUID) (state, note string, cancelled bool, sequence int64, started *bool) {
		t.Helper()
		if err := f.st.Pool.QueryRow(ctx, "SELECT o.state,o.note,o.cancel_requested,o.report_sequence,r.delivery_started FROM execution_operations o JOIN pipeline_runs r ON r.id=o.id WHERE o.id=$1", id).Scan(&state, &note, &cancelled, &sequence, &started); err != nil {
			t.Fatal(err)
		}
		return
	}
	// A hydrated command carries the provider credential as its Access block.
	writeAccess := func(t *testing.T, c *api.ExecutionCommand) (pipeline.Command, bool) {
		t.Helper()
		var command pipeline.Command
		if err := json.Unmarshal(*c.Pipeline, &command); err != nil {
			t.Fatal(err)
		}
		return command, command.Access != nil && command.Access.API.Password == writeCredential
	}

	// A runner runs one command at a time, so the next submission only dispatches
	// once the rejected one has left the queue.
	neverDelivered := map[string]func(t *testing.T, path string){
		"known value": func(t *testing.T, path string) {
			b := *b
			b.t = t
			b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "never-delivered-canary"}, 201, nil)
		},
		"over the validation limit": func(t *testing.T, path string) {
			b := *b
			b.t = t
			for i := range 17 {
				b.call("POST", path+"/secrets", map[string]string{"name": "LARGE_" + strconv.Itoa(i), "value": strings.Repeat("v", 64<<10)}, 201, nil)
			}
		},
	}
	for name, makeSecret := range neverDelivered {
		t.Run("never delivered "+name+" finishes on the controller", func(t *testing.T) {
			b := *b
			b.t = t
			canary := "never-delivered-canary"
			path, draft := prepare(t, "Never delivered "+name, inContent(canary))
			healthyPath, healthyDraft := prepare(t, "Healthy after "+name, inContent("not-a-secret"))
			rejected := dispatched(t, path, draft)
			makeSecret(t, path)
			healthy := submit(t, healthyPath, healthyDraft)
			command := poll(t)
			if command == nil || command.Id != healthy.Id {
				t.Fatal("rejected submission held the queue or reached the runner")
			}
			if _, access := writeAccess(t, command); !access {
				t.Fatal("clean submission lost its write credential")
			}
			state, note, _, sequence, started := operation(t, rejected.Id)
			if state != "failed" || sequence != 0 || started == nil || *started || !strings.Contains(note, "runtime secret") || strings.Contains(note, canary) {
				t.Fatal("rejected submission was not finished cleanly", state, note, sequence, started)
			}
			report(t, command, 1, "cancelled", "", nil)
			// Retrying the saved draft meets the same check at admission.
			b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": draft.Id, "requestKey": uuid.New()}, 400, nil)
		})
	}

	for _, mode := range []string{"delivered without report", "delivered file name", "legacy unknown", "running", "needs attention"} {
		t.Run(mode+" stops without credentials", func(t *testing.T) {
			b := *b
			b.t = t
			canary := "possibly-delivered-canary-" + strings.ReplaceAll(mode, " ", "-")
			plant := inContent(canary)
			if mode == "delivered file name" {
				plant = inFileName(canary)
			}
			path, draft := prepare(t, "Possibly "+mode, plant)
			op := dispatched(t, path, draft)
			command := poll(t)
			if command == nil || command.Id != op.Id {
				t.Fatal("submission did not reach the runner", command)
			}
			if _, access := writeAccess(t, command); !access {
				t.Fatal("first delivery lacked its write credential")
			}
			const cleanup = "Runner cleanup failed."
			next := int64(1)
			switch mode {
			case "legacy unknown":
				if _, err := f.st.Pool.Exec(ctx, "UPDATE pipeline_runs SET delivery_started=NULL WHERE id=$1", op.Id); err != nil {
					t.Fatal(err)
				}
			case "running":
				report(t, command, next, "running", "", nil)
				next++
			case "needs attention":
				report(t, command, next, "needs_attention", cleanup, nil)
				next++
			}
			b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, nil)
			again := poll(t)
			if again == nil || again.Id != op.Id {
				t.Fatal("the stop did not reach the runner", again)
			}
			frozen, access := writeAccess(t, again)
			if access || frozen.Access != nil || !again.CancelRequested {
				t.Fatal("possibly delivered submission received credentials after its files became secret", again.CancelRequested, frozen.Access != nil)
			}
			state, note, cancelled, _, _ := operation(t, op.Id)
			if state == "failed" || state == "cancelled" || state == "succeeded" || !cancelled {
				t.Fatal("possibly delivered submission was settled without its runner", state, cancelled)
			}
			branch := "astilba/" + frozen.SubmissionID.String()
			if !strings.Contains(note, branch) || strings.Contains(note, canary) {
				t.Fatal("stop did not warn about possible disclosure", note)
			}
			if mode == "needs attention" && !strings.Contains(note, cleanup) {
				t.Fatal("stop hid the cleanup instruction", note)
			}
			// A cleanup instruction joins the warning; the agent's other wording does not.
			report(t, again, next, "needs_attention", cleanup, nil)
			_, withCleanup, _, _, _ := operation(t, op.Id)
			if !strings.Contains(withCleanup, branch) || !strings.HasSuffix(withCleanup, cleanup) {
				t.Fatal("report hid the warning or the cleanup instruction", withCleanup)
			}
			report(t, again, next+1, "succeeded", "", &pipeline.Result{Commit: head, ReviewNumber: 7, ReviewURL: provider.URL + "/team/app/pulls/7"})
			final, settled, _, _, _ := operation(t, op.Id)
			if final != "cancelled" || !strings.Contains(settled, branch) || strings.Contains(settled, cleanup) {
				t.Fatal("stopped submission lost its warning or kept a stale instruction", final, settled)
			}
		})
	}

	// These run last because an accepted submission holds the runner from here on.
	t.Run("admission refuses a file name holding a runtime secret", func(t *testing.T) {
		b := *b
		b.t = t
		canary := "admission-file-name-canary"
		path, draft := prepare(t, "Admitted file name", inFileName(canary))
		b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, nil)
		refusal := string(b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": draft.Id, "requestKey": uuid.New()}, 400, nil))
		if !strings.Contains(refusal, "source.containsRuntimeSecret") || strings.Contains(refusal, canary) {
			t.Fatal("admission did not refuse the file name, or echoed the value", refusal)
		}
	})
	t.Run("a draft can rename a secret file name out of its baseline", func(t *testing.T) {
		b := *b
		b.t = t
		canary := "baseline-file-name-canary"
		rename := func(bundle *composeproject.Bundle) { bundle.Files[1].Path = "config/app.env" }
		path, draft := prepare(t, "Renamed baseline", rename, composeproject.File{Path: "config/" + canary + ".env", Content: "MODE=plain\n"})
		b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, nil)
		submit(t, path, draft)
	})
}
