package integration

import (
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	bolt "go.etcd.io/bbolt"
)

// Restrictions are delivered first, so a host that hasn't yet taken a deployment's links command can
// take a removal sent after it, at a later generation, and then never take the links command. It
// must end, so the deployment releases its peers, and the removal must still reach the host.
func TestSupersededLinksCommandEndsTheDeployment(t *testing.T) {
	g, op, e, journal := overtakenLinks(t)
	a, b := g.hosts[0], g.hosts[1]
	g.release(b, "links")
	links := g.linksOutcome(t, op, b, "failed")
	if !strings.Contains(links, "later command") {
		t.Fatalf("the links command's note hides why it ended: %q", links)
	}
	g.endedAndReleased(t, op, a, b, journal, "failed")
	// Nor is it wedged: it takes the next deployment.
	g.networkReady(t)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
}

// After an earlier agent has written to the journal, or the agent stopped uncleanly, the host can't
// tell whether it ever ran an overtaken command, so it refuses it. The owner's Cancel still ends
// it, and the deployment releases its peers.
func TestCancelEndsALinksCommandOfUnknownOutcome(t *testing.T) {
	g, op, e, _ := overtakenLinks(t)
	a, b := g.hosts[0], g.hosts[1]
	journal := g.restartJournal(t, b, func(dir string) {
		db, err := bolt.Open(dir+"/journal.db", 0600, nil)
		if err != nil {
			t.Fatal(err)
		}
		// What an earlier agent does as it opens the journal.
		if err = db.Update(func(tx *bolt.Tx) error { _, e := tx.CreateBucketIfNotExists([]byte("operations")); return e }); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	})
	g.mu.Lock()
	refused := g.refused[b.id]
	g.mu.Unlock()
	g.release(b, "links")
	g.waitFor(t, "the host to refuse the links command", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.refused[b.id] >= refused+3
	})
	if state, _ := g.linksState(t, op, b); state != "dispatched" {
		t.Fatalf("a links command of unknown outcome ended %s without a cancel", state)
	}
	g.b.call("POST", g.base+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
	if note := g.linksOutcome(t, op, b, "cancelled"); !strings.Contains(note, "may have run") {
		t.Fatalf("the cancelled links command's note claims more than the host knows: %q", note)
	}
	g.endedAndReleased(t, op, a, b, journal, "cancelled")
	g.networkReady(t)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
}

// overtakenLinks starts a deployment whose links command one host hasn't taken, then revokes another
// member, which sends that host a removal at a later generation. It returns once the host has taken
// the removal, with the links command still waiting.
func overtakenLinks(t *testing.T) (*revokedGroup, api.Operation, primaryEnvironment, *agent.Journal) {
	t.Helper()
	g := newRevokedGroup(t, 3)
	a, b, gone := g.hosts[0], g.hosts[1], g.hosts[2]
	journal := g.journaled(t, b)
	e := g.primaryEnvironment(t, a)
	// The host is offline when its links command is first delivered.
	g.hold(b, "links")
	op := g.apply(t, &e, g.reviewed(t, &e, g.place(t, &e, a.id, b.id)))
	g.waitFor(t, "the host to be sent its links command", func() bool { return g.received(b, "links") })
	// Revoking another member sends the reserved host a removal, at a later generation.
	g.revoke(t, gone)
	var removal api.ExecutionCommand
	g.waitFor(t, "the removal to reach the host", func() bool {
		if err := g.f.execution.SweepNetworks(g.ctx); err != nil {
			t.Fatal(err)
		}
		var raw []byte
		err := g.f.st.Pool.QueryRow(g.ctx, `SELECT command FROM execution_operations WHERE server_id=$1 AND kind='network' AND parent_id IS NULL AND command->'network'->>'action'='restrict' AND state='succeeded' AND created_at>(SELECT created_at FROM execution_operations WHERE id=$2)`, b.id, op.Id).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return false
		}
		if err != nil || json.Unmarshal(raw, &removal) != nil {
			t.Fatal("removal", err)
		}
		return true
	})
	var command hostnetwork.Command
	if err := hostnetwork.Decode(*removal.Network, &command); err != nil {
		t.Fatal(err)
	}
	if taken := g.journalRecord(t, journal, removal.Id); taken.Report.State != "succeeded" || slices.ContainsFunc(command.Manifest.Peers, func(p hostnetwork.Peer) bool { return p.ServerID == gone.id }) {
		t.Fatalf("the host still accepts the revoked server's key: %s", taken.Report.State)
	}
	return g, op, e, journal
}

func (g *revokedGroup) linksState(t *testing.T, op api.Operation, b *aheadHost) (state, note string) {
	t.Helper()
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT state,note FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND kind='network' AND command->'network'->>'action'='configure'`, op.Id, b.id).Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	return state, note
}

// linksOutcome waits for the host's links command to end as want, and returns its note.
func (g *revokedGroup) linksOutcome(t *testing.T, op api.Operation, b *aheadHost, want string) string {
	t.Helper()
	for until := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		state, note := g.linksState(t, op, b)
		if state == want {
			return note
		}
		if time.Now().After(until) {
			g.mu.Lock()
			refused := g.refused[b.id]
			g.mu.Unlock()
			g.dumpOperation(t, op.Id)
			t.Fatalf("the links command is still %s after the host's journal refused it %d times", state, refused)
		}
	}
}

// endedAndReleased requires the deployment to end as want, with both hosts told to clean up, no
// reservation left and nothing unfinished in the journaled host's journal.
func (g *revokedGroup) endedAndReleased(t *testing.T, op api.Operation, a, b *aheadHost, journal *agent.Journal, want string) {
	t.Helper()
	if state := g.finished(t, op.Id); state != want {
		t.Fatalf("a deployment whose links command never ran ended %s", state)
	}
	if !g.received(a, "abort") || !g.received(b, "abort") {
		t.Fatal("the hosts were not told to clean up")
	}
	if g.reservations(t) != 0 {
		t.Fatal("reservations outlived the deployment")
	}
	g.waitFor(t, "the host to settle everything it was sent", func() bool {
		pending, err := journal.Pending()
		if err != nil {
			t.Fatal(err)
		}
		return len(pending) == 0
	})
}

func (g *revokedGroup) journalRecord(t *testing.T, journal *agent.Journal, id uuid.UUID) agent.Record {
	t.Helper()
	record, err := journal.Record(id)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
