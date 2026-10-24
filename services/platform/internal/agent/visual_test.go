package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func testVisualJournal(t *testing.T) (*Journal, deployment.Plan) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	plan := deployment.Plan{ID: uuid.New(), ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Images: map[string]string{}, Specification: domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}}, ExpiresAt: time.Now().Add(time.Minute)}.WithHash()
	if err = j.SetConfig(Config{ServerID: plan.ServerID, Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	return j, plan
}
func visualCommand(plan deployment.Plan, action string, generation int64) api.ExecutionCommand {
	payload := deployment.Payload{Action: action, Plan: plan}
	raw, _ := json.Marshal(payload)
	message := json.RawMessage(raw)
	return api.ExecutionCommand{Id: uuid.New(), ServerId: plan.ServerID, EnvironmentId: plan.EnvironmentID, ResourceId: plan.UnitID, Kind: "visual", Action: "inspect", Visual: &message, PlanHash: payload.Digest(), Generation: generation, Deadline: time.Now().Add(time.Minute)}
}

func TestObservationFailureDoesNotBlockSubsequentCommands(t *testing.T) {
	j, plan := testVisualJournal(t)
	available := false
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			if !available {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"message":"transient failure"}`))
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
	engine := Engine{Journal: j, Docker: docker}
	first := visualCommand(plan, "observe", 1)
	if err = engine.Execute(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	failed, err := j.Record(first.Id)
	if err != nil || failed.Report.State != "failed" || failed.Report.Observation != nil {
		t.Fatal("failed observation became unreportable success", err)
	}
	if err = j.Acknowledge(first.Id, failed.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	available = true
	second := visualCommand(plan, "observe", 2)
	if err = engine.Execute(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	done, err := j.Record(second.Id)
	if err != nil || done.Report.State != "succeeded" || done.Report.Observation == nil {
		t.Fatal("recovered daemon could not complete a new observation", err)
	}
}

func TestKnownValuesAreRedactedBeforeLogTruncation(t *testing.T) {
	values := map[string]string{"short": "abc", "secret": "abcdefghij", "encoded": "a/b c", "prefix": "token=a"}
	input := strings.Repeat("x", 507) + "abcdefghij a%2Fb%20c"
	for range 30 {
		got, err := readVisualLog(strings.NewReader(input), true, values, 512)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(got, "[reda") {
			t.Fatalf("secret prefix reached truncated log: %q", got[500:])
		}
	}
	got := redactValues("abcdefghij a%2Fb%20c token=abcdefghij", values)
	if got != "[redacted] [redacted] [redacted]" {
		t.Fatalf("overlapping replacements leaked: %s", got)
	}
	if got, err := readVisualLog(strings.NewReader(strings.Repeat("x", 1<<20)+"abcdefghij"), true, values, 512); err == nil || got != "" {
		t.Fatal("incomplete oversized sample was exposed")
	}
}

func TestResolutionReportKeepsCapacityWithoutDuplicatedImages(t *testing.T) {
	j, plan := testVisualJournal(t)
	command := visualCommand(plan, "resolve", 1)
	if _, err := j.Accept(command); err != nil {
		t.Fatal(err)
	}
	observed := deployment.Observation{ObservedAt: time.Now(), Images: map[string]string{}, StateHash: strings.Repeat("a", 64), Resources: []deployment.Resource{}}
	image := "registry.example.test/" + strings.Repeat("a", 162) + "@sha256:" + strings.Repeat("b", 64)
	for i := range 40 {
		id := uuid.New()
		observed.Resources = append(observed.Resources, deployment.Resource{ID: id, Kind: "service", State: "running", Image: image, Drift: i == 0})
		if i < 20 {
			observed.Images[id.String()] = image
		}
	}
	compact := resolutionObservation(observed)
	if !compact.ResourceDrift || len(compact.Images) != 20 || len(compact.Resources) != 0 {
		t.Fatal("resolution lost drift or immutable image identities")
	}
	if err := (&Engine{Journal: j}).visualObservation(command, compact); err != nil {
		t.Fatal("valid full-capacity resolution could not be reported", err)
	}
	observed.Images = nil
	if err := (&Engine{Journal: j}).visualObservation(command, observed); err != nil {
		t.Fatal("partial runtime resource set could not be reported", err)
	}
}

// Cancelled replacements can leave more resources than a bounded report displays.
func TestRetainedResourcesDoNotBlockInspectionOrResolution(t *testing.T) {
	j, plan := testVisualJournal(t)
	var items []map[string]any
	labels := map[string]map[string]string{}
	for range 45 {
		id := uuid.New().String()
		labels[id] = visualLabels(plan, uuid.MustParse(id))
		items = append(items, map[string]any{"Id": id, "Labels": labels[id]})
	}
	if err := j.saveUnit(unitState{Plan: plan, Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		for id, label := range labels {
			if strings.HasSuffix(r.URL.Path, "/containers/"+id+"/json") {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Id": id, "Image": "sha256:example", "Config": map[string]any{"Image": "example.test/app:1", "Labels": label},
					"State": map[string]any{"Running": true, "Status": "running"}, "HostConfig": map[string]any{}, "NetworkSettings": map[string]any{},
				})
				return
			}
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
	engine := Engine{Journal: j, Docker: docker}
	var stateHash string
	for i, action := range []string{"observe", "resolve"} {
		command := visualCommand(plan, action, int64(i+1))
		if err := engine.Execute(context.Background(), command); err != nil {
			t.Fatal(err)
		}
		record, err := j.Record(command.Id)
		if err != nil || record.Report.State != "succeeded" || record.Report.Observation == nil {
			t.Fatal("retained resources blocked recovery", err)
		}
		var observed deployment.Observation
		if err := json.Unmarshal(*record.Report.Observation, &observed); err != nil {
			t.Fatal(err)
		}
		if !observed.ResourceDrift {
			t.Fatal("omitted resources lost their drift signal")
		}
		if action == "observe" {
			if len(observed.Resources) != 40 || observed.OmittedResources != 5 {
				t.Fatal("runtime report is not bounded and explicit")
			}
			stateHash = observed.StateHash
		} else if observed.StateHash != stateHash || len(observed.Resources) != 0 {
			t.Fatal("resolution lost the complete observed state")
		}
		if err := j.Acknowledge(command.Id, record.Report.Sequence); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOldComposeRevisionLogsAreWithheldWithoutItsSecrets(t *testing.T) {
	j, p := testVisualJournal(t)
	id := uuid.New()
	p.Specification.Compose = &composeproject.Bundle{}
	p.Specification.Applications = []domain.Application{{ID: id}}
	p = p.WithHash()
	labels := visualLabels(p, id)
	labels["com.astilba.plan"] = "older-plan"
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"Id": "old-container", "Labels": labels}})
		case strings.HasSuffix(r.URL.Path, "/containers/old-container/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "old-container", "Image": "sha256:example", "Config": map[string]any{"Image": "example.test/app:1", "Labels": labels, "Tty": true, "Env": []string{"TOKEN_FILE=/run/secrets/token"}}, "State": map[string]any{"Running": true, "Status": "running"}, "HostConfig": map[string]any{}, "NetworkSettings": map[string]any{}})
		case strings.HasSuffix(r.URL.Path, "/logs"):
			_, _ = w.Write([]byte("OLD-FILE-SECRET-CANARY\n"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer daemon.Close()
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	observed, _, err := (&Engine{Journal: j, Docker: docker}).observeUnit(context.Background(), p, map[string]string{"new": "NEW-SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Resources) != 1 || observed.Resources[0].Logs != "" || !strings.Contains(observed.Resources[0].Note, "withheld") {
		t.Fatalf("old file-secret logs not withheld: %+v", observed)
	}
}
