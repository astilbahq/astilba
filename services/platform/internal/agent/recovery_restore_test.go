package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	v := backup.Recovery{Installation: backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "S3", Kind: "s3", Endpoint: "https://s3.example.test", Bucket: "backups"}, Access: backup.Access{AccessKey: "key", SecretKey: "secret", Password: "repository-password"}}, RunID: uuid.New(), Snapshot: strings.Repeat("a", 64), TargetID: uuid.New()}
	if change != nil {
		change(&v)
	}
	raw, _ := stablejson.Marshal(v)
	value := jsontext.Value(raw)
	return api.ExecutionCommand{Id: uuid.New(), ServerId: plan.ServerID, EnvironmentId: plan.EnvironmentID, ResourceId: policy.ID, Kind: "restore", Action: "inspect", Generation: 1, PlanHash: v.Digest(), Backup: &value, Deadline: time.Now().Add(time.Hour)}, v
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
}

// A restore stopped after its first effect, whose containers can't be confirmed gone, is left to
// inspect without the repository's access, which nothing uses again. It has no schedule on this
// host: resolve-run settles its command once nothing labelled with its run is running, and only
// for its own policy and run.
func TestResolveRunSettlesARestoreLeftToInspect(t *testing.T) {
	j, plan := testVisualJournal(t)
	state := "exited"
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			// A container that removal never takes away.
			_, _ = w.Write([]byte(`[{"Id":"left","Names":["/left"],"State":"` + state + `"}]`))
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
	if err = e.checkpoint(c.Id, recoveryEffect); err != nil {
		t.Fatal(err)
	}
	// The agent starts again.
	if err = e.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	r, err := j.Record(c.Id)
	if err != nil || r.Report.State != "needs_attention" || !strings.Contains(r.Report.Note, "container left could not be removed") {
		t.Fatalf("ended %s: %s %v", r.Report.State, r.Report.Note, err)
	}
	var kept backup.Recovery
	if err = json.Unmarshal(*r.Command.Backup, &kept); err != nil || kept.Installation.Access != (backup.Access{}) || kept.Digest() != c.PlanHash {
		t.Fatal("the restore left to inspect kept its access: ", err)
	}
	policy := v.Installation.Policy.ID
	if err = e.ResolveScheduledRun(context.Background(), policy, uuid.New()); err == nil {
		t.Fatal("another run was resolved")
	}
	if err = e.ResolveScheduledRun(context.Background(), uuid.New(), v.RunID); err == nil {
		t.Fatal("another policy's run was resolved")
	}
	state = "running"
	if err = e.ResolveScheduledRun(context.Background(), policy, v.RunID); err == nil {
		t.Fatal("a run with a running container was resolved")
	}
	state = "exited"
	if err = e.ResolveScheduledRun(context.Background(), policy, v.RunID); err != nil {
		t.Fatal(err)
	}
	r, err = j.Record(c.Id)
	if err != nil || r.Report.State != "failed" || r.Report.Note != resolvedRunNote {
		t.Fatalf("ended %s: %s %v", r.Report.State, r.Report.Note, err)
	}
}
