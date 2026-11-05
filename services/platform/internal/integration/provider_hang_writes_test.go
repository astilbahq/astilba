package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// Operator writes ask the provider between passes too, and their outcome follows who the
// caller is after the call. A provider failure keeps its response, and audits nothing.
func TestBuildAdmissionRechecksAfterAHungProvider(t *testing.T) {
	for _, tc := range []struct {
		name       string
		during     func(t *testing.T, h *hungBuild)
		status     int
		operations int
	}{
		{name: "nothing changed", during: func(*testing.T, *hungBuild) {}, status: 202, operations: 1},
		{
			name: "caller lost write access",
			during: func(t *testing.T, h *hungBuild) {
				execSQL(t, h, "UPDATE memberships SET role='viewer' WHERE workspace_id=$1", h.ws)
			},
			status: 403,
		},
		{
			name:   "provider failed",
			during: func(t *testing.T, h *hungBuild) { h.provider.failReleased() },
			status: 400,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungBuild(t)
			entered, release := h.provider.hold(t, "/branches/main")
			finished := h.b.send("POST", h.path+"/builds", map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": uuid.New(), "configuration": h.config})
			waitEntered(t, entered)
			h.requireNothingHeld(t, uuid.Nil)
			tc.during(t, h)
			release()
			body := requireReply(t, <-finished, tc.status)
			if tc.name == "provider failed" && !bytes.Contains(body, []byte("branch")) {
				t.Fatal("a provider failure changed its response", string(body))
			}
			var operations, audits int
			if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM execution_operations WHERE workspace_id=$1),(SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='build.create')", h.ws).Scan(&operations, &audits); err != nil {
				t.Fatal(err)
			}
			if operations != tc.operations || audits != tc.operations {
				t.Fatal("admission outcome", operations, audits, "want", tc.operations)
			}
		})
	}
}

// Without the lock across the call, retries of one request all reach the provider. The key
// read under the lock still admits it once.
func TestConcurrentBuildRetriesAdmitOnceAfterAHungProvider(t *testing.T) {
	h := newHungBuild(t)
	entered, release := h.provider.hold(t, "/branches/main")
	key := uuid.New()
	body := map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": key, "configuration": h.config}
	results := make([]<-chan reply, 4)
	for i := range results {
		results[i] = h.b.send("POST", h.path+"/builds", body)
	}
	for range results {
		waitEntered(t, entered)
	}
	release()
	ids := map[uuid.UUID]bool{}
	for _, result := range results {
		var op api.Operation
		if err := json.Unmarshal(requireReply(t, <-result, 202), &op); err != nil {
			t.Fatal(err)
		}
		ids[op.Id] = true
	}
	var operations int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations WHERE request_key=$1", key).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || operations != 1 {
		t.Fatal("concurrent retries were admitted more than once", ids, operations)
	}
}

func TestConnectionCreationHoldsNothingDuringItsCheck(t *testing.T) {
	h := newHungBuild(t)
	var before int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM git_connections WHERE workspace_id=$1", h.ws).Scan(&before); err != nil {
		t.Fatal(err)
	}
	entered, release := h.provider.hold(t, "/repos/team/app")
	finished := h.b.send("POST", h.prefix+"/git-connections", map[string]any{"name": "Second", "provider": "gitea", "baseUrl": h.provider.server.URL, "repository": "team/app"})
	waitEntered(t, entered)
	h.requireNothingHeld(t, uuid.Nil)
	execSQL(t, h, "UPDATE memberships SET role='viewer' WHERE workspace_id=$1", h.ws)
	release()
	requireReply(t, <-finished, 403)
	var after int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM git_connections WHERE workspace_id=$1", h.ws).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("a connection was created for a caller who lost access during the check")
	}
}

func (h *hungBuild) push(t *testing.T, branch, commit string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"ref": "refs/heads/" + branch, "after": commit, "repository": map[string]int{"id": 12}})
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(raw)
	request, _ := http.NewRequest("POST", h.f.server.URL+"/hooks/git/"+h.connection.ID.String(), bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gitea-Event", "push")
	request.Header.Set("X-Gitea-Delivery", uuid.NewString())
	request.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := h.f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatal("push rejected", response.StatusCode)
	}
}

// A push handled during an admission's provider call used to wait for the workspace, and so
// always saw what the admission committed. Now its refresh can finish first, finding no
// target yet, and nothing would look at the branch again. The delivery makes the admission
// ask again instead, so it commits the commit it was told about.
func TestPushDuringBuildAdmissionIsNotLost(t *testing.T) {
	h := newHungBuild(t)
	config := h.config
	config.AutoBuild = true
	entered, release := h.provider.holdOnce(t, "/branches/main")
	finished := h.b.send("POST", h.path+"/builds", map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": uuid.New(), "configuration": config})
	waitEntered(t, entered)
	newer := strings.Repeat("c", 40)
	h.provider.head.Store(newer)
	h.push(t, "main", newer)
	deadline := time.Now().Add(15 * time.Second)
	for {
		var unfinished, finished int
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FILTER(WHERE finalized_at IS NULL),count(*) FILTER(WHERE finalized_at IS NOT NULL) FROM river_job WHERE kind='platform_git_refresh_v1'").Scan(&unfinished, &finished); err != nil {
			t.Fatal(err)
		}
		if unfinished == 0 && finished == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the push's refresh never finished", unfinished, finished)
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()
	requireReply(t, <-finished, 202)
	var requested, automatic string
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT requested_commit,automatic_requested_commit FROM build_targets WHERE workspace_id=$1", h.ws).Scan(&requested, &automatic); err != nil {
		t.Fatal(err)
	}
	if requested != newer || automatic != newer {
		t.Fatal("the admission recorded a commit older than the push", requested, automatic)
	}
}

// A busy repository must not keep an admission asking about a branch nobody pushed to.
func TestPushToAnotherBranchKeepsTheAdmissionsAnswer(t *testing.T) {
	h := newHungBuild(t)
	entered, release := h.provider.holdOnce(t, "/branches/main")
	finished := h.b.send("POST", h.path+"/builds", map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": uuid.New(), "configuration": h.config})
	waitEntered(t, entered)
	h.push(t, "other", strings.Repeat("c", 40))
	release()
	requireReply(t, <-finished, 202)
	if asked := h.provider.requests("/branches/main"); asked != 1 {
		t.Fatal("a push to another branch discarded the answer", asked)
	}
}
