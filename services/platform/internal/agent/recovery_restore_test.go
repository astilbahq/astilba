package agent

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func recoveryCommand(t *testing.T, plan deployment.Plan, change func(*backup.Recovery)) (api.ExecutionCommand, backup.Recovery) {
	t.Helper()
	policy := backup.Policy{ID: uuid.New(), Name: "Files", WorkspaceID: uuid.New(), EnvironmentID: plan.EnvironmentID, ServerID: plan.ServerID, DestinationID: uuid.New(), ResourceID: uuid.New(), Generation: 1, Method: backup.QuiescedFiles, Schedule: backup.Schedule{Timezone: "UTC"}, Retain: 1, Plan: plan}
	v := backup.Recovery{Installation: backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "S3", Kind: "s3", Endpoint: "https://s3.example.test", Bucket: "backups"}, Access: backup.Access{AccessKey: "key", SecretKey: "secret", Password: "repository-password"}}, RunID: uuid.New(), Snapshot: strings.Repeat("a", 64), TargetID: uuid.New(), ReceivedAt: time.Now().UTC()}
	if change != nil {
		change(&v)
	}
	raw, _ := stablejson.Marshal(v)
	value := jsontext.Value(raw)
	return api.ExecutionCommand{Id: uuid.New(), ServerId: plan.ServerID, EnvironmentId: plan.EnvironmentID, ResourceId: policy.ID, Kind: "restore", Action: "inspect", Generation: 1, PlanHash: v.Digest(), Backup: &value, Deadline: time.Now().Add(time.Hour)}, v
}

// An agent from before the restore kind refuses it before any effect, but the refusal is an error
// its command loop retries, not an outcome: the command stays running and unacknowledged, and holds
// the host's schedules. So management issues the kind only to an agent that advertises
// recoveryRestore.
func TestAnAgentWithoutTheRestoreKindRefusesItBeforeAnyEffect(t *testing.T) {
	j, plan := testVisualJournal(t)
	rule := commandKinds["restore"]
	delete(commandKinds, "restore")
	t.Cleanup(func() { commandKinds["restore"] = rule })
	c, _ := recoveryCommand(t, plan, nil)
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Journal: j}
	if err := e.Execute(context.Background(), c); err == nil || err.Error() != "unsupported command" {
		t.Fatal("an older agent ran the restore kind: ", err)
	}
	r, err := j.Record(c.Id)
	if err != nil || r.Report.State != "running" || r.Checkpoint != "accepted" || len(r.Report.Steps) != 0 || r.StartedAt != nil {
		t.Fatalf("an older agent began the restore: %+v %v", r, err)
	}
}

func TestRestoreRefusesACommandThatDoesNotIdentifyItsPolicy(t *testing.T) {
	j, plan := testVisualJournal(t)
	e := &Engine{Journal: j}
	generation := int64(0)
	for name, change := range map[string]func(*api.ExecutionCommand){
		"another plan hash":   func(c *api.ExecutionCommand) { c.PlanHash = strings.Repeat("0", 64) },
		"another resource":    func(c *api.ExecutionCommand) { c.ResourceId = uuid.New() },
		"another environment": func(c *api.ExecutionCommand) { c.EnvironmentId = uuid.New() },
	} {
		c, _ := recoveryCommand(t, plan, nil)
		generation++
		c.Generation = generation
		change(&c)
		if err := e.Execute(context.Background(), c); err != nil {
			t.Fatal(name, err)
		}
		r, err := j.Record(c.Id)
		if err != nil || r.Report.State != "failed" || r.Report.Note != "Restore command identity or policy is invalid." || r.StartedAt != nil {
			t.Fatalf("%s: %s %s %v", name, r.Report.State, r.Report.Note, err)
		}
		if err = j.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := recoveryCommand(t, plan, func(v *backup.Recovery) { v.TargetID = v.Installation.Policy.ResourceID })
	c.Generation = generation + 1
	if err := e.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if r, err := j.Record(c.Id); err != nil || r.Report.State != "failed" {
		t.Fatal("a restore into the live volume was accepted: ", err)
	}
}

// A restore left to inspect has no schedule on this host. resolve-run settles its command once
// nothing labelled with its run is running, and only for its own policy and run.
func TestResolveRunSettlesARestoreLeftToInspect(t *testing.T) {
	j, plan := testVisualJournal(t)
	running := false
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			if running {
				_, _ = w.Write([]byte(`[{"Id":"left","Names":["/left"],"State":"running"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"absent"}`))
	}))
	defer daemon.Close()
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	e := &Engine{Journal: j, Docker: docker}
	c, v := recoveryCommand(t, plan, nil)
	if _, err = j.Accept(c); err != nil {
		t.Fatal(err)
	}
	if err = j.Mutate(c.Id, func(r *Record) { settleReport(r, "needs_attention", "left to inspect") }); err != nil {
		t.Fatal(err)
	}
	policy := v.Installation.Policy.ID
	if err = e.ResolveScheduledRun(context.Background(), policy, uuid.New()); err == nil {
		t.Fatal("another run was resolved")
	}
	if err = e.ResolveScheduledRun(context.Background(), uuid.New(), v.RunID); err == nil {
		t.Fatal("another policy's run was resolved")
	}
	running = true
	if err = e.ResolveScheduledRun(context.Background(), policy, v.RunID); err == nil {
		t.Fatal("a run with a running container was resolved")
	}
	running = false
	if err = e.ResolveScheduledRun(context.Background(), policy, v.RunID); err != nil {
		t.Fatal(err)
	}
	r, err := j.Record(c.Id)
	if err != nil || r.Report.State != "failed" || r.Report.Note != resolvedRunNote {
		t.Fatalf("ended %s: %s %v", r.Report.State, r.Report.Note, err)
	}
}
