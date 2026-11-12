package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// networkCommands counts every network command management has ever written for a host.
// Operation rows are permanent history, so this is what a stuck member costs the workspace.
func (n *aheadNetwork) networkCommands(t *testing.T, h *aheadHost) int64 {
	t.Helper()
	return claimCounter(t, n.f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network'`, h.id)
}

// A host that is not prepared for managed networking fails every network command without
// changing anything, and its heartbeat says it can't take part. Management notes the member and
// sends nothing more until the host reports it is ready, rather than a new command on every sweep
// pass. Once it reports ready, it gets its manifest at once.
func TestUnpreparedHostIsHeldUntilItReportsReady(t *testing.T) {
	n := newAheadNetwork(t, 3)
	a, b, c := n.hosts[0], n.hosts[1], n.hosts[2]
	n.save(t, a, b)
	n.converge(t)
	a.unprepared = true
	n.beat = time.Time{}
	n.save(t, a, b, c)
	n.until(t, func() bool { return strings.Contains(n.note(t, a), "not prepared for managed networking") })
	sent := n.networkCommands(t, a)
	for range 5 {
		n.round(t)
	}
	if got := n.networkCommands(t, a); got != sent {
		t.Fatalf("%d more network commands went to a host that reports it is not prepared", got-sent)
	}
	// The other members carry on.
	n.until(t, func() bool { return holdsPeer(b, c.id) && holdsPeer(c, b.id) })

	a.unprepared = false
	n.beat = time.Time{}
	n.round(t)
	n.until(t, func() bool { return holdsPeer(a, c.id) && n.note(t, a) == "" })
	n.converge(t)
}

// A member reporting a key another member holds is refused with a note. Asking again would get
// the same key, so it isn't asked until the network changes, here because the member holding the
// key is revoked, and then it is asked at once.
func TestMemberWithAnotherMembersKeyIsAskedAgainOnlyWhenTheNetworkChanges(t *testing.T) {
	n := newAheadNetwork(t, 3)
	a, b, c := n.hosts[0], n.hosts[1], n.hosts[2]
	n.save(t, a, b)
	n.converge(t)
	c.copiesKey = a
	n.save(t, a, b, c)
	n.until(t, func() bool { return strings.Contains(n.note(t, c), "same private network key") })
	sent := n.networkCommands(t, c)
	for range 5 {
		n.round(t)
	}
	if got := n.networkCommands(t, c); got != sent {
		t.Fatalf("%d more identify commands went to a member whose key another member holds", got-sent)
	}
	if n.key(t, c) != "" {
		t.Fatal("management recorded a key another member holds")
	}

	c.copiesKey = nil
	n.revoke(t, a)
	n.until(t, func() bool { return n.key(t, c) != "" && holdsPeer(c, b.id) && holdsPeer(b, c.id) })
}

// The sweep holds a member for a reason of its own, apart from the note, which shows the owner the
// most important thing about the member. Another note doesn't release the hold.
func TestUnpreparedHoldDoesNotDependOnTheNote(t *testing.T) {
	n := newAheadNetwork(t, 3)
	a, b, c := n.hosts[0], n.hosts[1], n.hosts[2]
	n.save(t, a, b)
	n.converge(t)
	a.unprepared = true
	n.beat = time.Time{}
	n.save(t, a, b, c)
	n.until(t, func() bool {
		return claimCounter(t, n.f, `SELECT count(*) FROM network_members WHERE server_id=$1 AND sweep_hold='unprepared'`, a.id) == 1
	})
	if _, err := n.f.st.Pool.Exec(n.ctx, `UPDATE network_members SET note='Another reason the owner should read first.' WHERE server_id=$1`, a.id); err != nil {
		t.Fatal(err)
	}
	sent := n.networkCommands(t, a)
	for range 5 {
		n.round(t)
	}
	if got := n.networkCommands(t, a); got != sent {
		t.Fatalf("%d more network commands went to a held host once its note changed", got-sent)
	}
	if note := n.note(t, a); note != "Another reason the owner should read first." {
		t.Fatalf("the hold replaced the member's note: %q", note)
	}
}

// Migration 00027 left members whose shared key it couldn't award with the refusal's note but no
// refusal, so they identify again and the first to report keeps its key.
func TestMemberNotedByTheKeyMigrationIdentifiesAgain(t *testing.T) {
	n := newAheadNetwork(t, 2)
	a, b := n.hosts[0], n.hosts[1]
	n.save(t, a, b)
	n.converge(t)
	// What 00027 writes for a key it can't award: no key, its note and nothing else.
	if _, err := n.f.st.Pool.Exec(n.ctx, `UPDATE network_members SET public_key='',note='This server reported the same private network key as another member. It must report its own key before private links can finish.' WHERE server_id=$1`, b.id); err != nil {
		t.Fatal(err)
	}
	n.until(t, func() bool { return n.key(t, b) != "" && n.note(t, b) == "" })
}

// Revoking a server takes the workspace lock, then writes its network member and then its server
// row. A report of that server's failed network command must queue behind it rather than lock the
// server row first and then wait on the member, which would deadlock.
func TestReceiptlessNetworkFailureWaitsForTheWorkspaceLock(t *testing.T) {
	n := newAheadNetwork(t, 2)
	a, b := n.hosts[0], n.hosts[1]
	a.unprepared = true
	n.save(t, a, b)
	if err := n.f.execution.SweepNetworks(n.ctx); err != nil {
		t.Fatal(err)
	}
	var c *api.ExecutionCommand
	for until := time.Now().Add(20 * time.Second); c == nil; {
		poll, done := context.WithTimeout(n.ctx, aheadPollWait)
		got, err := pollReleased(n.f.execution, poll, a.identity)
		done()
		if err != nil && !waitForFixturePollLease(n.ctx, err) && poll.Err() == nil {
			t.Fatal(err)
		}
		c = got
		if time.Now().After(until) {
			t.Fatal("the host was sent no network command")
		}
	}
	held, err := n.f.st.Pool.Begin(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(n.ctx)
	if _, err = held.Exec(n.ctx, `SELECT id FROM workspaces WHERE id=$1 FOR UPDATE`, n.ws); err != nil {
		t.Fatal(err)
	}
	reported := make(chan error, 1)
	go func() {
		reported <- n.f.execution.Report(n.ctx, a.identity, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, State: "failed", Note: "Managed networking requires explicit supported-host preparation.", Sequence: 1, Steps: []api.ExecutionStep{}})
	}()
	select {
	case err = <-reported:
		t.Fatalf("the report was applied while another transaction held the workspace: %v", err)
	case <-time.After(time.Second):
	}
	if err = held.Commit(n.ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-reported; err != nil {
		t.Fatal(err)
	}
}

// Recovery rewrites network members from its bases and host evidence, so a sweep hold the restored
// database recorded describes none of them and must not keep removals from a recovered member.
// Projecting the recovered roots' grants advances the network generation, which releases it.
func TestRecoveryReleasesNetworkSweepHolds(t *testing.T) {
	r := newClaimRecovery(t, 7)
	if _, err := r.f.st.Pool.Exec(r.ctx, `UPDATE network_members SET sweep_hold='duplicate-key' WHERE workspace_id=$1`, r.ws); err != nil {
		t.Fatal(err)
	}
	r.recoverAll(t, r.exports(t, claimExport{}))
	if held := claimCounter(t, r.f, `SELECT count(*) FROM network_members WHERE workspace_id=$1 AND sweep_hold IS NOT NULL`, r.ws); held != 0 {
		t.Fatalf("%d recovered members are still held", held)
	}
}
