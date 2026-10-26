package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type scheduleTransport func(*http.Request) (*http.Response, error)

func (f scheduleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func scheduledJobFixture(t *testing.T, cleanupFailure string) (*Engine, backup.Policy, *int) {
	j, p := testVisualJournal(t)
	app := uuid.New()
	image := execution.TestImage
	p.Specification.Applications = []domain.Application{{ID: app, Name: "Worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Command: []string{"sleep", "86400"}}}
	p.Images = map[string]string{app.String(): image}
	p = p.WithHash()
	policy := backup.Policy{ID: uuid.New(), Name: "Worker", WorkspaceID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, ResourceID: app, Generation: 1, Method: "container-job", Command: []string{"sleep", "20"}, Schedule: backup.Schedule{Kind: "interval", IntervalMinutes: 1, Timezone: "UTC"}, TimeoutSeconds: 1, Retain: 1, Enabled: true, Plan: p}
	if err := policy.Validate(); err != nil {
		t.Fatal("fixture policy invalid", err)
	}
	created, jobInspects := 0, 0
	var jobLabels map[string]string
	source := map[string]any{"Id": "source", "Image": image, "Config": map[string]any{"Image": image, "Labels": visualLabels(p, app)}, "HostConfig": map[string]any{}, "State": map[string]any{"Running": true, "Status": "running"}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			json.NewEncoder(w).Encode([]any{map[string]any{"Id": "source", "Labels": visualLabels(p, app), "Names": []string{"/" + p.ContainerName(app)}}})
		case strings.HasSuffix(path, "/containers/source/json"):
			json.NewEncoder(w).Encode(source)
		case strings.HasSuffix(path, "/containers/create"):
			var b struct{ Labels map[string]string }
			json.NewDecoder(r.Body).Decode(&b)
			jobLabels = b.Labels
			created++
			jobInspects = 0
			w.WriteHeader(201)
			fmt.Fprint(w, `{"Id":"job","Warnings":[]}`)
		case strings.HasSuffix(path, "/containers/job/start"):
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/containers/job/stop"):
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/containers/job/json"):
			jobInspects++
			if cleanupFailure == "inspect" && jobInspects == 2 {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"message":"cleanup inspect failed"}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Id": "job", "Config": map[string]any{"Labels": jobLabels}, "State": map[string]any{"Running": false, "Status": "exited", "ExitCode": 0}})
		case strings.HasSuffix(path, "/containers/job") && r.Method == "DELETE":
			if cleanupFailure == "remove" {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"message":"cleanup remove failed"}`)
				return
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"absent"}`)
		}
	})
	transport := scheduleTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})
	docker, err := client.New(client.WithHost("http://docker-fixture"), client.WithHTTPClient(&http.Client{Transport: transport}), client.WithAPIVersion("1.56"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	e := &Engine{Journal: j, Docker: docker}
	stored := p
	stored.ID = uuid.Nil
	if err = j.saveUnit(unitState{Plan: stored}); err != nil {
		t.Fatal(err)
	}
	_, expected, err := e.observeUnit(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.saveUnit(unitState{Plan: p, Expected: expected}); err != nil {
		t.Fatal(err)
	}
	return e, policy, &created
}

func TestHydratedScheduleCancellationPreservesAcceptedCredentials(t *testing.T) {
	for _, kind := range []api.ExecutionCommandKind{"backup", "schedule"} {
		t.Run(string(kind), func(t *testing.T) {
			j, p := testVisualJournal(t)
			b := backup.Command{Action: "backup", Installation: backup.Installation{Policy: backup.Policy{ID: uuid.New()}, Access: backup.Access{Password: "journal-credential-canary"}}}
			if kind == "schedule" {
				b.Installation.Policy.Schedule = backup.Schedule{Kind: "interval", IntervalMinutes: 1, Timezone: "UTC"}
			}
			raw, _ := json.Marshal(b)
			payload := json.RawMessage(raw)
			c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: b.Installation.Policy.ID, Kind: kind, Generation: 1, PlanHash: b.Digest(), Backup: &payload, Deadline: time.Now().Add(time.Minute)}
			if _, err := j.Accept(c); err != nil {
				t.Fatal(err)
			}
			b.Installation.Access = backup.Access{}
			raw, _ = json.Marshal(b)
			bare := json.RawMessage(raw)
			c.Backup = &bare
			c.CancelRequested = true
			record, err := j.Accept(c)
			if err != nil || !record.Command.CancelRequested || !strings.Contains(string(*record.Command.Backup), "journal-credential-canary") {
				t.Fatal("credential-free cancellation lost accepted intent/access", err)
			}
			b.Installation.Policy.TimeoutSeconds = 5
			raw, _ = json.Marshal(b)
			changed := json.RawMessage(raw)
			c.Backup = &changed
			if _, err = j.Accept(c); err == nil {
				t.Fatal("cancellation changed frozen execution inputs")
			}
		})
	}
}
func TestManualAdvancedRunRejectsSupersedingLegacyMirror(t *testing.T) {
	j, p := testVisualJournal(t)
	policy := backup.Policy{ID: uuid.New(), Name: "Job", WorkspaceID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, ResourceID: uuid.New(), Generation: 1, Method: "container-job", Command: []string{"true"}, Schedule: backup.Schedule{Kind: "interval", IntervalMinutes: 1, Timezone: "UTC"}, TimeoutSeconds: 1, Retain: 1, Enabled: true, Plan: p}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: policy, Next: time.Now().Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	if err := j.db.Update(func(tx *bolt.Tx) error {
		var mirror scheduleState
		if err := j.get(tx, "schedules", policy.ID.String(), &mirror); err != nil {
			return err
		}
		mirror.Sequence++
		return j.put(tx, "schedules", policy.ID.String(), mirror)
	}); err != nil {
		t.Fatal(err)
	}
	b := backup.Command{Action: "backup", Installation: backup.Installation{Policy: policy}, RunID: uuid.New()}
	raw, _ := json.Marshal(b)
	payload := json.RawMessage(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: policy.ID, Kind: "schedule", Generation: 2, PlanHash: b.Digest(), Backup: &payload, Deadline: time.Now().Add(time.Minute)}
	e := Engine{Journal: j}
	if err := e.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	record, err := j.Record(c.Id)
	if err != nil || record.Report.State != "failed" {
		t.Fatal("manual mirror execution was admitted", err)
	}
	state, err := j.schedule(policy.ID)
	if err != nil || len(state.Runs) != 0 {
		t.Fatal("manual execution created effects before current installation", err)
	}
}
func TestKnownJobExitDoesNotReleaseUnconfirmedCleanupFence(t *testing.T) {
	for _, failure := range []string{"inspect", "remove"} {
		t.Run(failure, func(t *testing.T) {
			e, p, created := scheduledJobFixture(t, failure)
			at := time.Now().UTC()
			if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				now := at.Add(time.Duration(i) * time.Minute)
				if err := e.tickSchedules(context.Background(), func() time.Time { return now }); err != nil {
					t.Fatal(err)
				}
			}
			state, err := e.Journal.schedule(p.ID)
			if err != nil || *created != 1 || len(state.Runs) != 1 || state.Runs[0].State != "needs_attention" || state.Runs[0].ExitCode == nil {
				t.Fatal("known exit discarded the cleanup fence", err)
			}
		})
	}
}
