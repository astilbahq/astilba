package integration

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// aheadPollWait is short because a command the sweep just queued reaches a poll within a round or
// two, and an idle host would otherwise hold up the others in the same round.
const aheadPollWait = 100 * time.Millisecond

// aheadHost answers management as an agent does, by the host's own rule for what it may install.
type aheadHost struct {
	id         uuid.UUID
	token      string
	input      api.AgentEnrollment
	identity   execution.Identity
	store      hostnetwork.Host
	held       hostnetwork.Manifest
	controller int64
	member     api.ManagedNetworkMemberInput
	refused    int
	silent     bool
	// stale drops what it polls, as a journal does for a counter it has passed.
	stale bool
}

type aheadNetwork struct {
	f      *fixture
	b      *browser
	ctx    context.Context
	ws     uuid.UUID
	base   string
	hosts  []*aheadHost
	beat   time.Time
	backup struct {
		generation int64
		members    string
	}
}

func newAheadNetwork(t *testing.T, count int) *aheadNetwork {
	t.Helper()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	n := &aheadNetwork{f: f, b: b, ctx: context.Background(), ws: ws, base: "/api/v1/workspaces/" + ws.String()}
	for i := range count {
		enrolled, in, token := enrollment(t, f, b, ws, "deployment")
		yes := true
		in.Capabilities.CrossHost, in.Capabilities.MaintenanceContinuation, in.Capabilities.Visual = &yes, &yes, &yes
		in.Preflight.CrossHostInventory = &api.CrossHostInventory{CpuMillis: 4000, MemoryMiB: 8192, UnboundedInventoryHash: hostnetwork.StableInventory(nil)}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
		identity, err := f.execution.Authenticate(n.ctx, enrolled.Server.Id, strings.SplitN(token, ".", 2)[1])
		if err != nil {
			t.Fatal(err)
		}
		n.hosts = append(n.hosts, &aheadHost{id: enrolled.Server.Id, token: token, input: in, identity: identity, store: hostnetwork.Host{Dir: t.TempDir()}, member: api.ManagedNetworkMemberInput{ServerId: enrolled.Server.Id, Endpoint: fmt.Sprintf("192.0.2.%d:51820", i+1), ListenPort: 51820, CpuMillis: 2000, MemoryMiB: 4096, UnboundedInventoryHash: hostnetwork.StableInventory(nil)}})
	}
	return n
}

// round sweeps, then answers one poll for every host that is listening. Sweeping here keeps each
// step from waiting on the worker's five second interval.
func (n *aheadNetwork) round(t *testing.T) {
	t.Helper()
	if err := n.f.execution.SweepNetworks(n.ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(n.beat) > 10*time.Second {
		for _, h := range n.hosts {
			if h.silent {
				continue
			}
			if _, err := n.f.execution.Heartbeat(n.ctx, h.identity, api.AgentHeartbeat{Capabilities: h.input.Capabilities, Preflight: h.input.Preflight}); err != nil {
				t.Fatal(err)
			}
		}
		n.beat = time.Now()
	}
	for _, h := range n.hosts {
		if h.silent {
			continue
		}
		poll, done := context.WithTimeout(n.ctx, aheadPollWait)
		c, err := n.f.execution.Poll(poll, h.identity)
		expired := poll.Err()
		done()
		if err != nil {
			if waitForFixturePollLease(n.ctx, err) || expired != nil {
				continue
			}
			t.Fatal(err)
		}
		if c == nil {
			continue
		}
		var command hostnetwork.Command
		if err = hostnetwork.Decode(*c.Network, &command); err != nil {
			t.Fatal(err)
		}
		// An invalid command would wedge a real host, whose report of it management refuses.
		if err = command.Validate(); err != nil {
			t.Fatalf("host was sent an invalid command: %+v: %v", command, err)
		}
		if h.stale && command.Action == "configure" {
			continue
		}
		// The agent's own fence: it takes a manifest only for the controller it was transferred to.
		if command.Action == "configure" && command.Manifest.ControllerGeneration != h.controller {
			t.Fatalf("host at controller %d was sent a manifest for controller %d", h.controller, command.Manifest.ControllerGeneration)
		}
		report := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}}
		receipt, err := fixtureNetworkReceipt(h.store, *c, &h.held, nil, h.controller)
		if errors.Is(err, hostnetwork.ErrGenerationConflict) || err != nil && command.Action == "restrict" {
			h.refused++
			report.State = "failed"
			report.Network = claimMessage(t, hostnetwork.Receipt{Identity: receipt.Identity, NetworkID: receipt.NetworkID, Refused: true})
		} else if err != nil {
			t.Fatal(err)
		} else {
			report.Network = claimMessage(t, receipt)
		}
		if err = n.f.execution.Report(n.ctx, h.identity, report); err != nil {
			t.Fatal(err)
		}
	}
}

func (n *aheadNetwork) view(t *testing.T) (out struct {
	Generation int64  `json:"generation"`
	State      string `json:"state"`
}) {
	t.Helper()
	n.b.call("GET", n.base+"/network", nil, 200, &out)
	return out
}

func (n *aheadNetwork) save(t *testing.T, hosts ...*aheadHost) {
	t.Helper()
	members := []api.ManagedNetworkMemberInput{}
	for _, h := range hosts {
		members = append(members, h.member)
	}
	in := api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", Members: members}
	in.BaseGeneration = n.view(t).Generation
	n.b.call("PUT", n.base+"/network", in, 200, nil)
}

func (n *aheadNetwork) converge(t *testing.T) {
	t.Helper()
	for until := time.Now().Add(70 * time.Second); ; {
		n.round(t)
		if view := n.view(t); view.State == "ready" {
			return
		} else if time.Now().After(until) {
			rows, err := n.f.st.Pool.Query(n.ctx, `SELECT m.server_id::text,m.enabled,m.public_key<>'',m.generation,m.acknowledged_generation,m.approved_controller,s.controller_generation,m.note,COALESCE((SELECT o.state||' '||COALESCE(o.command->'network'->>'action','') FROM execution_operations o WHERE o.server_id=m.server_id ORDER BY o.created_at DESC LIMIT 1),'') FROM network_members m JOIN servers s ON s.id=m.server_id WHERE m.workspace_id=$1`, n.ws)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var server, note, last string
				var enabled, keyed bool
				var generation, acknowledged, approved, controller int64
				if err = rows.Scan(&server, &enabled, &keyed, &generation, &acknowledged, &approved, &controller, &note, &last); err != nil {
					t.Fatal(err)
				}
				t.Logf("%s enabled=%v keyed=%v generation=%d acknowledged=%d approved=%d controller=%d note=%q last=%q", server, enabled, keyed, generation, acknowledged, approved, controller, note, last)
			}
			t.Fatalf("network never became ready: %+v", view)
		}
	}
}

func (n *aheadNetwork) counter(t *testing.T) int64 {
	t.Helper()
	return claimCounter(t, n.f, `SELECT generation FROM workspace_networks WHERE workspace_id=$1`, n.ws)
}

// takeBackup records the network tables as a management backup would hold them.
func (n *aheadNetwork) takeBackup(t *testing.T) {
	t.Helper()
	n.backup.generation = n.counter(t)
	var name string
	if err := n.f.st.Pool.QueryRow(n.ctx, `SELECT 'host_ahead_backup_'||replace($1::text,'-','')`, n.ws).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if _, err := n.f.st.Pool.Exec(n.ctx, `CREATE TABLE `+name+` AS SELECT * FROM network_members WHERE workspace_id='`+n.ws.String()+`'`); err != nil {
		t.Fatal(err)
	}
	n.backup.members = name
}

// restore puts the network tables back, then fences and transfers every host, which is what the
// documented recovery does. Only the host's local owner moves it to the new controller.
func (n *aheadNetwork) restore(t *testing.T) {
	t.Helper()
	if err := n.f.execution.Stop(n.ctx); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM network_members WHERE workspace_id='` + n.ws.String() + `'`,
		`INSERT INTO network_members SELECT * FROM ` + n.backup.members,
		fmt.Sprintf(`UPDATE workspace_networks SET generation=%d WHERE workspace_id='%s'`, n.backup.generation, n.ws),
	} {
		if _, err := n.f.st.Pool.Exec(n.ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range n.hosts {
		n.fence(t, h)
	}
	if err := n.f.execution.Start(n.ctx); err != nil {
		t.Fatal(err)
	}
	n.beat = time.Time{}
}

func (n *aheadNetwork) fence(t *testing.T, h *aheadHost) {
	t.Helper()
	if err := n.f.st.PrepareRecovery(n.ctx, h.id, h.controller+1); err != nil {
		t.Fatal(err)
	}
	var ticket api.TransferTicket
	n.b.call("POST", n.base+"/servers/"+h.id.String()+"/transfer", nil, 201, &ticket)
	credential, err := cryptobox.Token()
	if err != nil {
		t.Fatal(err)
	}
	generation := int64(ticket.Generation)
	input := h.input
	input.Token, input.Credential, input.ControllerGeneration = ticket.Token, credential, &generation
	input.Inventory = &api.ControllerInventory{Units: map[string]string{}, Schedules: map[string]int64{}}
	if _, err = n.f.execution.Enroll(n.ctx, input); err != nil {
		t.Fatal(err)
	}
	if h.identity, err = n.f.execution.Authenticate(n.ctx, h.id, credential); err != nil {
		t.Fatal(err)
	}
	h.input, h.controller, h.token = input, generation, h.id.String()+"."+credential
}

func (n *aheadNetwork) note(t *testing.T, h *aheadHost) string {
	t.Helper()
	var note string
	if err := n.f.st.Pool.QueryRow(n.ctx, `SELECT note FROM network_members WHERE server_id=$1`, h.id).Scan(&note); err != nil {
		t.Fatal(err)
	}
	return note
}

// revoke clears the server's credential at once. Its peers learn of it through the next sweep.
func (n *aheadNetwork) revoke(t *testing.T, h *aheadHost) {
	t.Helper()
	n.b.call("POST", n.base+"/servers/"+h.id.String()+"/revoke", nil, 204, nil)
	if status := claimStatus(t, n.f, "/agent/v1/heartbeat", h.token, api.AgentHeartbeat{Capabilities: h.input.Capabilities, Preflight: h.input.Preflight}); status != 401 {
		t.Fatalf("revoked credential heartbeat = %d", status)
	}
	h.silent = true
}

// until answers polls until the condition holds.
func (n *aheadNetwork) until(t *testing.T, done func() bool) {
	t.Helper()
	for until := time.Now().Add(70 * time.Second); !done(); n.round(t) {
		if time.Now().After(until) {
			t.Fatal("the hosts never reached the expected state")
		}
	}
}

// member reads the generation management wants this host to hold.
func (n *aheadNetwork) member(t *testing.T, h *aheadHost) int64 {
	t.Helper()
	return claimCounter(t, n.f, `SELECT generation FROM network_members WHERE server_id=$1`, h.id)
}

func holdsPeer(h *aheadHost, server uuid.UUID) bool {
	return slices.ContainsFunc(h.held.Peers, func(p hostnetwork.Peer) bool { return p.ServerID == server })
}

// A host far past anything a restored backup knows, holding a peer that backup never heard of,
// comes back through the ordinary approval. Its number moves nothing in management, and a
// revocation reaches it at every step.
func TestRestoredManagementRecoversAHostAhead(t *testing.T) {
	for _, test := range []struct {
		name string
		// held is what the host's generation becomes after the backup.
		held    func(int64) int64
		approve func(t *testing.T, n *aheadNetwork, a, b, added *aheadHost)
		// keeps is whether the save keeps b, and adds whether it adds a peer.
		keeps, adds bool
	}{
		{
			name: "additive save", keeps: true, adds: true,
			held: func(g int64) int64 { return g + 1<<40 },
			approve: func(t *testing.T, n *aheadNetwork, a, b, added *aheadHost) {
				n.save(t, a, b, added)
				n.converge(t)
			},
		},
		{
			name: "unchanged save", keeps: true,
			held: func(g int64) int64 { return g + 1<<40 },
			approve: func(t *testing.T, n *aheadNetwork, a, b, _ *aheadHost) {
				n.save(t, a, b)
				n.converge(t)
			},
		},
		{
			// A removal needs no approval, so it reaches a transferred host before the owner saves.
			name: "host at the bigint limit, peer revoked before the save",
			held: func(int64) int64 { return math.MaxInt64 },
			approve: func(t *testing.T, n *aheadNetwork, a, b, _ *aheadHost) {
				n.revoke(t, b)
				n.until(t, func() bool { return !holdsPeer(a, b.id) })
				n.save(t, a)
				// A revoked server can never acknowledge, so the network stays visibly pending
				// and only the member that remains can finish.
				n.until(t, func() bool { return !holdsPeer(a, n.hosts[2].id) && a.held.Generation == n.member(t, a) })
			},
		},
		{
			// The approved manifest must not wait for a peer that may never report a key.
			name: "new peer has no key", adds: true,
			held: func(g int64) int64 { return g + 1<<40 },
			approve: func(t *testing.T, n *aheadNetwork, a, b, added *aheadHost) {
				added.silent = true
				n.save(t, a, added)
				n.until(t, func() bool { return !holdsPeer(a, b.id) && !holdsPeer(a, n.hosts[2].id) })
				if note := n.note(t, a); note != "" {
					t.Fatalf("the save left a note: %q", note)
				}
				added.silent = false
				n.converge(t)
				// The first save after a restore told the member it removed, as any save does.
				if !b.held.Disabled || b.held.ControllerGeneration != b.controller {
					t.Fatalf("the removed member was not told: %+v", b.held)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			n := newAheadNetwork(t, 4)
			a, b, unknown, added := n.hosts[0], n.hosts[1], n.hosts[2], n.hosts[3]
			n.save(t, a, b)
			n.converge(t)
			n.takeBackup(t)
			n.save(t, a, b, unknown)
			n.converge(t)
			if !holdsPeer(a, unknown.id) {
				t.Fatal("the host never held the peer the backup does not know")
			}
			a.held.Generation = test.held(a.held.Generation)
			held := a.held.Generation
			n.restore(t)
			restored := n.counter(t)
			if restored >= held {
				t.Fatalf("the backup's counter %d is not behind the host's %d", restored, held)
			}
			unknown.silent = true
			test.approve(t, n, a, b, added)
			if holdsPeer(a, unknown.id) {
				t.Fatal("the host kept a peer the restored backup does not know")
			}
			if test.keeps != holdsPeer(a, b.id) || test.adds != holdsPeer(a, added.id) {
				t.Fatalf("the host did not reach the approved peers: %+v", a.held.Peers)
			}
			if a.held.ControllerGeneration != a.controller || a.held.Generation >= held {
				t.Fatalf("host holds controller %d generation %d", a.held.ControllerGeneration, a.held.Generation)
			}
			if a.refused != 0 {
				t.Fatalf("the host refused %d commands", a.refused)
			}
			if got := n.counter(t); got >= held || got > restored+receiptMargin {
				t.Fatalf("the counter moved from %d to %d because of what the host holds", restored, got)
			}
			// A revocation after the host's recovery reaches it through the same sweep.
			target := b
			if !test.keeps {
				target = added
				if !test.adds {
					return
				}
			}
			n.revoke(t, target)
			n.until(t, func() bool { return !holdsPeer(a, target.id) })
		})
	}
}

// Under a new controller a real host reports the generation it was sent. A compromised one can
// only lie, and neither a forged history nor a forged present moves the counter.
func TestHostileClaimsAfterRestoreMoveNothing(t *testing.T) {
	const history = int64(1) << 50
	for name, present := range map[string]bool{"forged present": true, "forged history": false} {
		t.Run(name, func(t *testing.T) {
			n := newAheadNetwork(t, 3)
			a, b, c := n.hosts[0], n.hosts[1], n.hosts[2]
			n.save(t, a, b, c)
			n.converge(t)
			n.takeBackup(t)
			n.restore(t)
			n.save(t, a, b, c)
			n.converge(t)
			n.save(t, a, b)
			a.silent = true
			var frame *api.ExecutionCommand
			var command hostnetwork.Command
			for until := time.Now().Add(70 * time.Second); frame == nil; {
				if time.Now().After(until) {
					t.Fatal("no removal reached the host")
				}
				n.round(t)
				if _, err := n.f.execution.Heartbeat(n.ctx, a.identity, api.AgentHeartbeat{Capabilities: a.input.Capabilities, Preflight: a.input.Preflight}); err != nil {
					t.Fatal(err)
				}
				poll, done := context.WithTimeout(n.ctx, aheadPollWait)
				delivered, err := n.f.execution.Poll(poll, a.identity)
				expired := poll.Err()
				done()
				if err != nil {
					if waitForFixturePollLease(n.ctx, err) || expired != nil {
						continue
					}
					t.Fatal(err)
				}
				if delivered != nil {
					if err = hostnetwork.Decode(*delivered.Network, &command); err != nil {
						t.Fatal(err)
					}
					if command.Action != "restrict" {
						t.Fatalf("expected a removal, got %s", command.Action)
					}
					frame = delivered
				}
			}
			issued := n.counter(t)
			controller := a.controller
			// A controller the host's owner never moved it to is refused outright.
			future := a.held
			future.ControllerGeneration, future.Generation = controller+1, history
			refused := hostnetwork.Receipt{Identity: hostnetwork.Identity{MembershipID: command.MembershipID, PublicKey: command.Manifest.PublicKey}, NetworkID: command.NetworkID, Before: &future, Effective: &future, Generation: future.Generation, Hash: hostnetwork.Hash(future)}
			if status := claimStatus(t, n.f, "/agent/v1/report", a.token, api.AgentReport{Id: frame.Id, Generation: frame.Generation, PlanHash: frame.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}, Network: claimMessage(t, refused)}); status == 204 {
				t.Fatal("a receipt naming a later controller was accepted")
			}
			before := a.held
			before.Generation = history
			if !present {
				before.ControllerGeneration = a.controller - 1
			}
			effective, err := hostnetwork.ReconcileRestriction(before, *command.Baseline, *command.Manifest, uuid.Nil, a.controller)
			if err != nil {
				t.Fatal(err)
			}
			receipt := hostnetwork.Receipt{Identity: hostnetwork.Identity{MembershipID: command.MembershipID, PublicKey: command.Manifest.PublicKey}, NetworkID: command.NetworkID, Before: &before, Effective: &effective, Generation: effective.Generation, Hash: hostnetwork.Hash(effective), Disabled: effective.Disabled}
			report := api.AgentReport{Id: frame.Id, Generation: frame.Generation, PlanHash: frame.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}, Network: claimMessage(t, receipt)}
			if status := claimStatus(t, n.f, "/agent/v1/report", a.token, report); status != 204 {
				t.Fatalf("report status = %d", status)
			}
			if got := n.counter(t); got != issued {
				t.Fatalf("a forged generation moved the counter from %d to %d", issued, got)
			}
			if got := claimCounter(t, n.f, `SELECT controller_generation FROM servers WHERE id=$1`, a.id); got != controller {
				t.Fatalf("a receipt moved the server's controller from %d to %d", controller, got)
			}
			if note := n.note(t, a); present != strings.Contains(note, "far beyond") {
				t.Fatalf("note = %q", note)
			}
			// Revocation still goes through, whatever the host claimed.
			n.revoke(t, a)
			if got := n.counter(t); got != issued+1 {
				t.Fatalf("revocation moved the counter to %d, want %d", got, issued+1)
			}
		})
	}
}

// Restoring management without fencing a host leaves it ahead under the same controller. Depending
// on the counters, it refuses a configure or drops it as stale. Either way removals still reach it
// and the sweep stops repeating itself, and fencing is the way out.
func TestUnfencedHostAheadRecoversThroughFencing(t *testing.T) {
	for name, stale := range map[string]bool{"refuses the configure": false, "drops the configure as stale": true} {
		t.Run(name, func(t *testing.T) {
			n := newAheadNetwork(t, 3)
			a, b, added := n.hosts[0], n.hosts[1], n.hosts[2]
			n.save(t, a, b)
			n.converge(t)
			a.held.Generation += 1 << 40
			a.stale = stale
			n.save(t, a, b, added)
			if !stale {
				n.until(t, func() bool { return a.refused > 0 && n.note(t, a) != "" })
				if !strings.Contains(n.note(t, a), "prepare-recovery") {
					t.Fatalf("note = %q", n.note(t, a))
				}
				// It would answer the same again, so nothing more goes to it, however many sweeps pass.
				sent := claimCounter(t, n.f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network'`, a.id)
				for range 3 {
					n.round(t)
				}
				if got := claimCounter(t, n.f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network'`, a.id); got != sent {
					t.Fatalf("%d more network commands went to the host after it refused", got-sent)
				}
			} else {
				n.until(t, func() bool {
					return claimCounter(t, n.f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network' AND state='dispatched'`, a.id) > 0
				})
			}
			issued := n.counter(t)
			// A revocation reaches the host while the change that adds a peer cannot.
			n.revoke(t, b)
			n.until(t, func() bool { return !holdsPeer(a, b.id) })
			if holdsPeer(a, added.id) {
				t.Fatal("a removal granted a peer")
			}
			if got := n.counter(t); got > issued+2 {
				t.Fatalf("the counter moved from %d to %d because of what the host holds", issued, got)
			}
			if err := n.f.execution.Stop(n.ctx); err != nil {
				t.Fatal(err)
			}
			n.fence(t, a)
			if err := n.f.execution.Start(n.ctx); err != nil {
				t.Fatal(err)
			}
			n.beat = time.Time{}
			a.stale = false
			refused := a.refused
			held := a.held.Generation
			n.save(t, a, added)
			n.until(t, func() bool { return holdsPeer(a, added.id) && n.note(t, a) == "" })
			if a.refused != refused || a.held.Generation >= held || a.held.ControllerGeneration != a.controller {
				t.Fatalf("the fenced host did not recover: refused %d, %+v", a.refused-refused, a.held)
			}
		})
	}
}

// A removal built from a record that no longer describes the host is refused, and would be refused
// again forever. Management forgets the record, and the whole manifest brings the host back, even
// while an approved new peer withholds its key.
func TestRefusedRemovalFallsBackToTheWholeManifest(t *testing.T) {
	n := newAheadNetwork(t, 4)
	a, b, c, silent := n.hosts[0], n.hosts[1], n.hosts[2], n.hosts[3]
	n.save(t, a, b, c)
	n.converge(t)
	approved := a.held.ListenPort
	a.held.ListenPort = approved + 1
	silent.silent = true
	n.save(t, a, b, c, silent)
	n.revoke(t, c)
	n.until(t, func() bool { return !holdsPeer(a, c.id) })
	if a.refused != 1 || a.held.ListenPort != approved {
		t.Fatalf("refused %d, host holds %+v", a.refused, a.held)
	}
	if !holdsPeer(a, b.id) {
		t.Fatal("the whole manifest dropped an approved peer")
	}
}

// A re-added member has a new membership. Its record of the old one must not shape what it is sent,
// and the whole manifest must not wait for a peer that has no key.
func TestReAddedMemberReceivesItsWholeManifest(t *testing.T) {
	n := newAheadNetwork(t, 4)
	a, b, c, silent := n.hosts[0], n.hosts[1], n.hosts[2], n.hosts[3]
	n.save(t, a, b, c)
	n.converge(t)
	n.save(t, a, b)
	n.converge(t)
	if !c.held.Disabled {
		t.Fatalf("the removed member was not told: %+v", c.held)
	}
	old := c.held.MembershipID
	silent.silent = true
	n.save(t, a, b, c, silent)
	n.until(t, func() bool { return c.held.MembershipID != old && holdsPeer(c, a.id) && holdsPeer(c, b.id) })
	if holdsPeer(c, silent.id) {
		t.Fatal("the member was given a peer with no key")
	}
	silent.silent = false
	n.converge(t)
	if !holdsPeer(c, silent.id) || !holdsPeer(a, silent.id) {
		t.Fatalf("the members did not reach the approved peers: %+v", c.held.Peers)
	}
}

// A manifest on its way already carries any removal. A sweep that also sent the removal, at the same
// generation, would reach the host first, because removals are delivered first, and the host would
// then refuse the manifest for changing what that generation holds.
func TestSweepWaitsForAManifestOnItsWay(t *testing.T) {
	n := newAheadNetwork(t, 2)
	a, b := n.hosts[0], n.hosts[1]
	n.save(t, a, b)
	n.converge(t)
	sent := func(action string) int64 {
		return claimCounter(t, n.f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network' AND command->'network'->>'action'=$2`, a.id, action)
	}
	removals := sent("restrict")
	// Changing a peer's endpoint removes the old peer and adds the new one.
	b.member.Endpoint = "192.0.2.77:51820"
	a.silent = true
	n.save(t, a, b)
	for range 3 {
		if err := n.f.execution.SweepNetworks(n.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := sent("restrict"); got != removals {
		t.Fatalf("%d removals went out alongside the manifest that carries them", got-removals)
	}
	a.silent = false
	n.converge(t)
	if a.refused != 0 {
		t.Fatalf("the host refused %d commands", a.refused)
	}
	// A manifest that has not landed must not hold a removal back for long.
	b.member.Endpoint = "192.0.2.78:51820"
	a.stale = true
	n.save(t, a, b)
	n.until(t, func() bool {
		return claimCounter(t, n.f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network' AND state='dispatched'`, a.id) > 0
	})
	if !holdsPeer(a, b.id) {
		t.Fatal("the host lost the peer before the wait ended")
	}
	if _, err := n.f.st.Pool.Exec(n.ctx, `UPDATE execution_operations SET created_at=created_at-interval '1 minute' WHERE server_id=$1 AND state='dispatched'`, a.id); err != nil {
		t.Fatal(err)
	}
	n.until(t, func() bool { return !holdsPeer(a, b.id) })
}
