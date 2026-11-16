package integration

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// discardOf is the latest discard the restore with id records, if any.
func (g *revokedGroup) discardOf(t *testing.T, l lostFiles, id uuid.UUID) (uuid.UUID, api.ExecutionState) {
	t.Helper()
	for _, r := range g.recoveries(l) {
		if r.Id == id {
			if r.Discard == nil {
				return uuid.Nil, ""
			}
			return r.Discard.OperationId, r.Discard.State
		}
	}
	t.Fatal("the restore isn't listed")
	return uuid.Nil, ""
}

// A restore's volume is either cut over to or discarded. While a discard is active the volume
// can't be switched in or discarded again, and the restore records the discard; a failed one
// leaves it usable. A draft, or a plan prepared from it, that mounts the volume keeps it, and a
// cutover and a discard sent together don't both succeed.
func TestACutoverAndADiscardExcludeEachOther(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.restoreCapable(t, &l)
	record := g.restored(t, l, "succeeded")
	if op, _ := g.discardOf(t, l, record.Id); op != uuid.Nil {
		t.Fatal("a restore records a discard it never had")
	}

	// An active discard holds the volume, and a failed one gives it back.
	g.hold(l.b, "visual-delete-volume")
	got := g.discard(l, record.Id)
	var op api.Operation
	if got.status != http.StatusCreated || json.Unmarshal(got.body, &op) != nil {
		t.Fatalf("discard = %d %s", got.status, got.body)
	}
	g.waitFor(t, "the discard to reach the server", func() bool { return g.received(l.b, "visual-delete-volume") })
	if recorded, state := g.discardOf(t, l, record.Id); recorded != op.Id || state == api.ExecutionStateSucceeded {
		t.Fatalf("the restore records discard %s (%s), not the active %s", recorded, state, op.Id)
	}
	draft := g.draft(t, l)
	if got := g.cutover(l, record.Id, draft.Id); got.status != http.StatusConflict {
		t.Fatalf("a cutover during a discard = %d %s", got.status, got.body)
	}
	if got := g.discard(l, record.Id); got.status != http.StatusConflict {
		t.Fatalf("a second discard during a discard = %d %s", got.status, got.body)
	}
	g.mu.Lock()
	g.failing["visual-delete-volume/"+l.b.id.String()] = true
	g.mu.Unlock()
	g.release(l.b, "visual-delete-volume")
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("discard ended %s", state)
	}
	g.mu.Lock()
	delete(g.failing, "visual-delete-volume/"+l.b.id.String())
	g.mu.Unlock()
	if _, state := g.discardOf(t, l, record.Id); state != api.ExecutionStateFailed {
		t.Fatalf("the restore records its discard %s", state)
	}

	// Two cutovers sent together from the same draft: the first moves the draft on, so the other
	// is refused. A cutover draft, and a plan prepared from it, keep the volume. A plan can't be
	// applied once the draft has moved on, so the draft decides.
	first := g.b.send("POST", recoveryPath(l, record.Id)+"/cutover", api.BackupRecoveryCutoverRequest{BaseRevisionId: draft.Id})
	second := g.b.send("POST", recoveryPath(l, record.Id)+"/cutover", api.BackupRecoveryCutoverRequest{BaseRevisionId: draft.Id})
	got, other := <-first, <-second
	if got.status == http.StatusConflict {
		got, other = other, got
	}
	var revision draftRevision
	if got.status != http.StatusOK || other.status != http.StatusConflict || json.Unmarshal(got.body, &revision) != nil {
		t.Fatalf("two cutovers after a failed discard = %d %s and %d %s", got.status, got.body, other.status, other.body)
	}
	g.b.call("GET", l.e.path+"/draft", nil, 200, &l.e.draft)
	plan := g.reviewed(t, &l.e, <-g.b.send("POST", l.e.path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: revision.Id, ServerId: l.b.id}))
	if state := g.finished(t, plan.OperationID); state != "succeeded" {
		t.Fatalf("plan ended %s", state)
	}
	if got := g.discard(l, record.Id); got.status != http.StatusConflict {
		t.Fatalf("a discard of a volume a draft and plan mount = %d %s", got.status, got.body)
	}
	g.b.call("PUT", l.e.path+"/draft", map[string]any{"baseRevisionId": revision.Id, "specification": draft.Specification}, 200, &revision)
	if got := <-g.b.send("POST", l.e.path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}); got.status != http.StatusConflict {
		t.Fatalf("applying a plan the draft moved on from = %d %s", got.status, got.body)
	}

	// Sent together, one of a cutover and a discard wins.
	g.hold(l.b, "visual-delete-volume")
	cutover := g.b.send("POST", recoveryPath(l, record.Id)+"/cutover", api.BackupRecoveryCutoverRequest{BaseRevisionId: revision.Id})
	discard := g.b.send("DELETE", recoveryPath(l, record.Id)+"/target", nil)
	switched, discarded := <-cutover, <-discard
	switch {
	case switched.status == http.StatusOK && discarded.status == http.StatusConflict:
		t.Log("the cutover won")
	case switched.status == http.StatusConflict && discarded.status == http.StatusCreated:
		t.Log("the discard won")
		if json.Unmarshal(discarded.body, &op) != nil {
			t.Fatal("invalid discard reply")
		}
		g.release(l.b, "visual-delete-volume")
		if state := g.finished(t, op.Id); state != "succeeded" {
			t.Fatalf("discard ended %s", state)
		}
		// A volume that is gone can't be switched in.
		if got := g.cutover(l, record.Id, revision.Id); got.status != http.StatusConflict {
			t.Fatalf("a cutover after a discard = %d %s", got.status, got.body)
		}
	default:
		t.Fatalf("a cutover and a discard together = %d %s and %d %s", switched.status, switched.body, discarded.status, discarded.body)
	}
	g.release(l.b, "visual-delete-volume")
}

// A restore's latest discard travels with its workspace: an export carries it and its operation,
// and an import keeps it. An archive from before discards were recorded imports with none.
func TestARestoresDiscardTravelsWithItsWorkspace(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.restoreCapable(t, &l)
	record := g.restored(t, l, "succeeded")
	g.mu.Lock()
	g.failing["visual-delete-volume/"+l.b.id.String()] = true
	g.mu.Unlock()
	var op api.Operation
	g.b.call("DELETE", recoveryPath(l, record.Id)+"/target", nil, 201, &op)
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("discard ended %s", state)
	}

	const passphrase = "unique archive passphrase for discards"
	var encrypted api.WorkspaceArchive
	g.b.call("POST", g.base+"/export", map[string]string{"passphrase": passphrase}, 200, &encrypted)
	plain, err := portability.Open(g.ctx, passphrase, encrypted.Archive)
	if err != nil {
		t.Fatal(err)
	}
	var archive portability.Archive
	if err = json.Unmarshal(plain, &archive); err != nil {
		t.Fatal(err)
	}
	carried := false
	for _, row := range archive.Tables["backup_recoveries"] {
		var id, discard uuid.UUID
		if json.Unmarshal(row["id"], &id) == nil && id == record.Id {
			carried = json.Unmarshal(row["discard_operation_id"], &discard) == nil && discard == op.Id
		}
	}
	exported := false
	for _, row := range archive.Tables["execution_operations"] {
		var id uuid.UUID
		exported = exported || json.Unmarshal(row["id"], &id) == nil && id == op.Id
	}
	if !carried || !exported {
		t.Fatalf("the export carries the discard %v and its operation %v", carried, exported)
	}

	imported := func(archive []byte) *uuid.UUID {
		t.Helper()
		destination := setup(t)
		owner := destination.browser(t)
		owner.login("owner@example.test", destination.password)
		owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": archive, "passphrase": passphrase}, 201, nil)
		var discard *uuid.UUID
		if err := destination.st.Pool.QueryRow(g.ctx, `SELECT discard_operation_id FROM backup_recoveries WHERE id = $1`, record.Id).Scan(&discard); err != nil {
			t.Fatal(err)
		}
		return discard
	}
	if discard := imported(encrypted.Archive); discard == nil || *discard != op.Id {
		t.Fatalf("the import recorded discard %v, not %s", discard, op.Id)
	}
	archive.Version = 15
	for _, row := range archive.Tables["backup_recoveries"] {
		delete(row, "discard_operation_id")
	}
	older, err := stablejson.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := portability.Seal(g.ctx, passphrase, older)
	if err != nil {
		t.Fatal(err)
	}
	if discard := imported(sealed); discard != nil {
		t.Fatalf("a version 15 archive was given discard %s", discard)
	}
}
