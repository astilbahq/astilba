package integration

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
)

// An owner who force-pushes a branch back to remove a commit, while a runner is importing that
// commit, must not be left with it. The refresh for the push back found the source already on
// the branch's commit and did nothing, then the runner's import was saved: the draft held the
// removed commit with nothing pending, and the next plan would deploy it.
func TestImportFollowsABranchMovedBackWhileItRan(t *testing.T) {
	i := newImportedEnvironment(t, "github")
	i.reviewed(t)
	good := i.source.head.Load().(string)
	bad := strings.Repeat("b", 40)
	removed := []composeproject.File{{Path: "compose.yaml", Content: "# removed\n" + importHonest[0].Content}, importHonest[1]}
	i.source.tree.record(bad, removed)
	i.source.push(bad)
	running := i.runner.next()

	var before int64
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT COALESCE(max(id),0) FROM river_job").Scan(&before); err != nil {
		t.Fatal(err)
	}
	i.source.push(good)
	// It finds the source already on the branch's commit, which is where it used to stop. A
	// short wait shows as available, because River promotes jobs ahead of their time.
	waitFor(t, "the refresh for the push back", 15*time.Second, func() bool {
		var looked int
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job WHERE kind='platform_git_refresh_v1' AND id>$1 AND attempted_at IS NOT NULL AND state IN ('available','scheduled','completed')", before).Scan(&looked); err != nil {
			t.Fatal(err)
		}
		return looked == 1
	})
	i.runner.reportImport(running, bad, removed)
	if op := i.operation(t, running.Id); op.State != "succeeded" {
		t.Fatalf("the import of a commit the provider holds was refused: %s %q", op.State, op.Note)
	}

	waitFor(t, "the import of the branch's commit", 20*time.Second, func() bool { return i.importOperations(t, good) == 2 })
	c := i.runner.next()
	var command pipeline.Command
	if err := json.Unmarshal(*c.Pipeline, &command); err != nil || command.Action != "import" || command.Commit != good {
		t.Fatal("the branch's commit was not imported", command.Action, command.Commit, err)
	}
	explained := "Imports commit " + good + " again because the branch moved back to it from commit " + bad + "."
	if op := i.operation(t, c.Id); !strings.Contains(op.Note, explained) {
		t.Fatalf("the import does not say why it ran: %q", op.Note)
	}
	i.runner.reportImport(c, good, importHonest)
	if op := i.operation(t, c.Id); op.State != "succeeded" || !strings.Contains(op.Note, explained) {
		t.Fatalf("the import of the branch's commit: %s %q", op.State, op.Note)
	}

	var imported, pending, status string
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT imported_commit,pending_commit,status FROM git_sources WHERE workspace_id=$1 AND environment_id=$2", i.ws, i.env.Id).Scan(&imported, &pending, &status); err != nil {
		t.Fatal(err)
	}
	_, spec := draftSpec(t, i.b, i.path)
	if imported != good || pending != "" || status != "ready" || spec.Compose.Origin.Commit != good || spec.Compose.Files[0].Content != importHonest[0].Content {
		t.Fatalf("the source did not return to the branch: imported=%s pending=%q status=%s draft=%s", imported, pending, status, spec.Compose.Origin.Commit)
	}
	var blocked string
	waitFor(t, "the removed commit's deployment to be decided", 15*time.Second, func() bool {
		if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT note FROM pipeline_auto_deployments WHERE id=$1", running.Id).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked != ""
	})
	if blocked != "The branch no longer points at this commit; this deployment was superseded." {
		t.Fatalf("the removed commit's deployment says %q", blocked)
	}
	var delivered int
	if err := i.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations WHERE server_id=$1 AND command::text LIKE '%# removed%'", i.server.Server.Id).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered != 0 {
		t.Fatal("the removed commit reached the deployment server")
	}
}
