package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// buildDeadlineRuns checks a build's deadline. One whose runner reported starting it by its
// deadline, after waiting behind other work on the host, may run for its lifetime past the
// deadline, though no values leave after it. One whose start management hears of only after
// the deadline is stopped, whatever the runner's clock read, and publishes nothing, as is one
// its runner lost after the deadline, though no other server can say so. admit queues a build
// on the runner token polls as, other is another server's token, and result is a success the
// runner may report.
func buildDeadlineRuns(t *testing.T, f *fixture, token, other string, admit func() uuid.UUID, poll func(uuid.UUID) api.ExecutionCommand, result pipeline.Result) {
	t.Helper()
	for _, run := range []string{"started", "unacknowledged", "rekeyed", "late", "lost"} {
		id := admit()
		c := poll(id)
		started := func() {
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "running", Steps: []api.ExecutionStep{{Name: pipeline.StartStep, State: "intent"}}}, 204, nil)
		}
		if run != "late" {
			started()
		}
		// The stored command, which holds no credentials, reaches its deadline.
		var raw []byte
		var frozen api.ExecutionCommand
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT command FROM execution_operations WHERE id=$1", c.Id).Scan(&raw); err != nil || json.Unmarshal(raw, &frozen) != nil {
			t.Fatal("stored command unreadable", err)
		}
		frozen.Deadline = time.Now().Add(-time.Second)
		raw, _ = stablejson.Marshal(frozen)
		if _, err := f.st.Pool.Exec(context.Background(), "UPDATE execution_operations SET command=$2 WHERE id=$1", c.Id, raw); err != nil {
			t.Fatal(err)
		}
		lost := func(token string) {
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/releases", token, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: c.Id, Epoch: c.SecretRelease.Epoch, Reason: api.JournalMissing}}}, 204, nil)
		}
		switch run {
		case "started":
			// Another server saying it lost the build changes nothing.
			lost(other)
		case "late":
			started()
		case "unacknowledged":
			// Its runner never acknowledged the values, so they are due again: it holds them.
			if _, err := f.st.Pool.Exec(context.Background(), "UPDATE secret_releases SET last_attempt_at=now()-interval '1 minute' WHERE operation_id=$1", c.Id); err != nil {
				t.Fatal(err)
			}
		case "rekeyed":
			// As after the runner's key changes: management holds no release for the instance
			// that polls, though the runner holds the build and goes on with it.
			if _, err := f.st.Pool.Exec(context.Background(), "DELETE FROM secret_releases WHERE operation_id=$1", c.Id); err != nil {
				t.Fatal(err)
			}
		case "lost":
			lost(token)
		}
		running := run == "started" || run == "unacknowledged" || run == "rekeyed"
		again := poll(id)
		if again.CancelRequested == running {
			t.Fatal("a build's cancellation did not follow when its start was reported", run)
		}
		if (run == "unacknowledged" || run == "rekeyed") && (again.SecretRelease == nil || again.SecretRelease.Values) {
			t.Fatal("a build delivered without its values after its deadline claimed to carry them", again.SecretRelease)
		}
		for _, value := range []string{"clone-read-canary", "build-only-canary", "push-canary"} {
			if strings.Contains(string(*again.Pipeline), value) {
				t.Fatal("a build's credentials left management after its deadline", run, value)
			}
		}
		raw, _ = stablejson.Marshal(result)
		msg := jsontext.Value(raw)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 3, State: "succeeded", Steps: []api.ExecutionStep{{Name: pipeline.StartStep, State: "done"}}, Pipeline: &msg}, 204, nil)
		var state string
		var count int
		_ = f.st.Pool.QueryRow(context.Background(), "SELECT state FROM execution_operations WHERE id=$1", id).Scan(&state)
		_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM build_artifacts WHERE id=$1", id).Scan(&count)
		if running && (state != "succeeded" || count != 1) || !running && (state != "cancelled" || count != 0) {
			t.Fatal("a build's result did not follow when its start was reported", run, state, count)
		}
	}
}
