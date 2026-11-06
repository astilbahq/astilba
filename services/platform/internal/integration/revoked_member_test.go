package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// startRevocableMember enrolls a host that only keeps its private links current.
func startRevocableMember(ctx context.Context, t *testing.T, f *fixture, b *browser, ws uuid.UUID, wg *sync.WaitGroup) (uuid.UUID, api.ManagedNetworkMemberInput) {
	t.Helper()
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.CrossHost = &yes
	inventory := hostnetwork.StableInventory(nil)
	in.Preflight.CrossHostInventory = &api.CrossHostInventory{CpuMillis: 4000, MemoryMiB: 8192, UnboundedInventoryHash: inventory}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	identity, err := f.execution.Authenticate(ctx, enrolled.Server.Id, strings.SplitN(token, ".", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	host := hostnetwork.Host{Dir: t.TempDir()}
	wg.Go(func() { answerNetworkUntilRevoked(ctx, t, f, identity, in, host) })
	return enrolled.Server.Id, api.ManagedNetworkMemberInput{ServerId: enrolled.Server.Id, Endpoint: "192.0.2.3:51820", ListenPort: 51820, CpuMillis: 2000, MemoryMiB: 4096, UnboundedInventoryHash: inventory}
}

// answerNetworkUntilRevoked is a host that does nothing but keep its private links current, and
// stops when management revokes it.
func answerNetworkUntilRevoked(ctx context.Context, t *testing.T, f *fixture, identity execution.Identity, in api.AgentEnrollment, host hostnetwork.Host) {
	var held hostnetwork.Manifest
	var heartbeat time.Time
	for ctx.Err() == nil {
		if time.Since(heartbeat) >= 15*time.Second {
			if _, err := f.execution.Heartbeat(ctx, identity, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}); err != nil {
				if !errors.Is(err, domain.Unauthenticated) && ctx.Err() == nil {
					t.Error(err)
				}
				return
			}
			heartbeat = time.Now()
		}
		poll, done := context.WithTimeout(ctx, time.Second)
		c, err := f.execution.Poll(poll, identity)
		expired := poll.Err()
		done()
		if errors.Is(err, domain.Unauthenticated) {
			return
		}
		if err != nil {
			if !waitForFixturePollLease(ctx, err) && expired == nil && ctx.Err() == nil {
				t.Error(err)
				return
			}
			continue
		}
		if c == nil {
			continue
		}
		receipt, err := fixtureNetworkReceipt(host, *c, &held, nil, 0)
		if err != nil {
			t.Error(err)
			return
		}
		report := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}, Network: claimMessage(t, receipt)}
		if err = f.execution.Report(ctx, identity, report); err != nil && !errors.Is(err, domain.Unauthenticated) && ctx.Err() == nil {
			t.Error(err)
			return
		}
	}
}

// revokedMember reads what the owner is shown about the network and one of its members.
func (n *aheadNetwork) revokedMember(t *testing.T, server uuid.UUID) (network, member, note string) {
	t.Helper()
	var view struct {
		State   string `json:"state"`
		Members []struct {
			ServerID uuid.UUID `json:"serverId"`
			State    string    `json:"state"`
			Note     string    `json:"note"`
		} `json:"members"`
	}
	n.b.call("GET", n.base+"/network", nil, 200, &view)
	for _, m := range view.Members {
		if m.ServerID == server {
			return view.State, m.State, m.Note
		}
	}
	t.Fatalf("server %s is not in the network", server)
	return "", "", ""
}

func (n *aheadNetwork) wantMember(t *testing.T, server uuid.UUID, network, member string) {
	t.Helper()
	if gotNetwork, gotMember, note := n.revokedMember(t, server); gotNetwork != network || gotMember != member {
		t.Fatalf("network %q member %q (%q), want %q and %q", gotNetwork, gotMember, note, network, member)
	}
}

// primaryEnvironment is an environment whose primary unit is already applied on the given host,
// which is where every placement starts from.
type primaryEnvironment struct {
	path  string
	draft api.Revision
	spec  domain.Specification
}

func (n *aheadNetwork) primaryEnvironment(t *testing.T, primary *aheadHost) primaryEnvironment {
	t.Helper()
	var project api.Project
	n.b.call("POST", n.base+"/projects", map[string]string{"name": "Placement"}, 201, &project)
	var environment api.Environment
	n.b.call("POST", n.base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Web"}, 201, &environment)
	path := n.base + "/projects/" + project.Id.String() + "/environments/" + environment.Id.String()
	image, app := execution.TestImage, uuid.New()
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "placement.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var draft api.Revision
	n.b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": environment.DraftRevisionId, "specification": spec}, 200, &draft)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := n.f.st.Pool.Exec(n.ctx, query, args...); err != nil {
			t.Fatal(query, err)
		}
	}
	unit := claimUUID(t, n.f, `SELECT id FROM deployment_units WHERE workspace_id=$1 AND environment_id=$2`, n.ws, environment.Id)
	exec(`UPDATE deployment_units SET server_id=$1 WHERE id=$2`, primary.id, unit)
	source := deployment.Plan{ID: uuid.New(), ServerID: primary.id, EnvironmentID: environment.Id, UnitID: unit, RevisionID: draft.Id, Recipe: deployment.Recipe, ExpiresAt: time.Now().Add(time.Hour), Specification: spec, Images: map[string]string{app.String(): image}}.WithHash()
	body := deployment.Payload{Action: "deploy", Plan: source}
	applied := api.ExecutionCommand{Id: uuid.New(), ServerId: primary.id, EnvironmentId: environment.Id, ResourceId: unit, Generation: 1, Kind: "visual", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), Visual: claimMessage(t, body)}
	exec(`INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence) VALUES($1,$2,$3,$4,1,$5,$6,'visual','succeeded',1)`, applied.Id, n.ws, primary.id, environment.Id, uuid.New(), claimBytes(t, applied))
	exec(`INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) VALUES($1,$2,$3,$4,$5,$6,$7,'{}')`, source.ID, n.ws, primary.id, environment.Id, unit, draft.Id, applied.Id)
	exec(`INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4)`, source.ID, n.ws, environment.Id, claimBytes(t, source))
	exec(`UPDATE execution_operations SET runtime_outcome='applied',runtime_plan_id=$2 WHERE id=$1`, applied.Id, source.ID)
	exec(`INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,observation) VALUES($1,$2,$3,$4,$5,'{}')`, n.ws, environment.Id, unit, primary.id, source.ID)
	exec(`UPDATE environments SET applied_revision_id=$2,runtime_operation_id=$3,execution_generation=2 WHERE id=$1`, environment.Id, draft.Id, applied.Id)
	return primaryEnvironment{path: path, draft: draft, spec: spec}
}

// place saves a placement on the targets and asks for its plan.
func (n *aheadNetwork) place(t *testing.T, e *primaryEnvironment, ingress uuid.UUID, targets ...uuid.UUID) reply {
	t.Helper()
	var network struct {
		ID uuid.UUID `json:"id"`
	}
	n.b.call("GET", n.base+"/network", nil, 200, &network)
	spec := e.spec
	placed := []domain.PlacementTarget{}
	for _, target := range targets {
		placed = append(placed, domain.PlacementTarget{ServerID: target, Count: 1})
	}
	spec.Placement = &domain.Placement{NetworkID: network.ID, IngressServerID: ingress, Applications: []domain.ApplicationPlacement{{ApplicationID: spec.Applications[0].ID, CPUMillis: 100, MemoryMiB: 64, Targets: placed}}}
	n.b.call("PUT", e.path+"/draft", map[string]any{"baseRevisionId": e.draft.Id, "specification": spec}, 200, &e.draft)
	return <-n.b.send("POST", e.path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: e.draft.Id, ServerId: ingress, AllowHostChanges: true})
}

// wantPlacementRefused requires the plan to be refused because of the network.
func wantPlacementRefused(t *testing.T, got reply) {
	t.Helper()
	if got.err != nil || got.status != http.StatusBadRequest || !strings.Contains(string(got.body), "placement.network") {
		t.Fatalf("placement while the network is pending = %d %s %v", got.status, got.body, got.err)
	}
}

// Removing a revoked server's access is its peers' work, so the network waits for each of them and
// for nothing else, and says so at every step.
func TestRevokedMemberHoldsTheNetworkOnlyThroughItsPeers(t *testing.T) {
	n := newAheadNetwork(t, 3)
	a, b, revoked := n.hosts[0], n.hosts[1], n.hosts[2]
	n.save(t, n.hosts...)
	n.converge(t)
	environment := n.primaryEnvironment(t, a)

	n.revoke(t, revoked)
	n.wantMember(t, revoked.id, "pending", "revocation_pending")
	if _, _, note := n.revokedMember(t, revoked.id); !strings.Contains(note, "may still hold its own") || !strings.Contains(note, "may still accept its key") {
		t.Fatalf("pending note = %q", note)
	}
	wantPlacementRefused(t, n.place(t, &environment, a.id, b.id))

	// One peer has dropped the key and the other has not heard, so the key still works on it.
	b.silent = true
	n.until(t, func() bool {
		return !holdsPeer(a, revoked.id) && claimCounter(t, n.f, `SELECT acknowledged_generation FROM network_members WHERE server_id=$1`, a.id) == n.member(t, a)
	})
	if !holdsPeer(b, revoked.id) {
		t.Fatal("the silent peer was expected to still hold the key")
	}
	n.wantMember(t, revoked.id, "pending", "revocation_pending")
	wantPlacementRefused(t, n.place(t, &environment, a.id, b.id))

	b.silent = false
	n.converge(t)
	if holdsPeer(a, revoked.id) || holdsPeer(b, revoked.id) {
		t.Fatal("the network is ready while a peer holds the revoked key")
	}
	n.wantMember(t, revoked.id, "ready", "removed")
	if _, _, note := n.revokedMember(t, revoked.id); !strings.Contains(note, "may still hold its own") || !strings.Contains(note, "none of them accepts it") {
		t.Fatalf("finished note = %q", note)
	}

	// Ready says nothing good about the revoked host, which no plan can name.
	if got := n.place(t, &environment, a.id, revoked.id); got.err != nil || got.status != http.StatusBadRequest || !strings.Contains(string(got.body), "placement.member") {
		t.Fatalf("placement on the revoked host = %d %s %v", got.status, got.body, got.err)
	}

	// The network lets the placement through. TestCrossHostCoordinatedPlacement carries one all the
	// way through its apply with a revoked member in the network.
	if got := n.place(t, &environment, a.id, b.id); got.err != nil || got.status != http.StatusCreated {
		t.Fatalf("placement after every peer acknowledged = %d %s %v", got.status, got.body, got.err)
	}

	// Nothing here went through the revoked host, which still cannot act.
	if count := claimCounter(t, n.f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND state NOT IN ('succeeded','failed','cancelled')`, revoked.id); count != 0 {
		t.Fatalf("%d commands waiting on the revoked host", count)
	}
	if status := claimStatus(t, n.f, "/agent/v1/heartbeat", revoked.token, api.AgentHeartbeat{Capabilities: revoked.input.Capabilities, Preflight: revoked.input.Preflight}); status != http.StatusUnauthorized {
		t.Fatalf("revoked credential heartbeat = %d", status)
	}
}

// Each later generation has to be acknowledged before the key can be called gone, because only
// that proves what a peer holds.
func TestRevokedMemberReadsPendingWhileAnythingIs(t *testing.T) {
	n := newAheadNetwork(t, 3)
	a, b, revoked := n.hosts[0], n.hosts[1], n.hosts[2]
	n.save(t, n.hosts...)
	n.converge(t)
	n.revoke(t, revoked)
	n.converge(t)
	n.wantMember(t, revoked.id, "ready", "removed")
	n.save(t, a, b)
	n.wantMember(t, revoked.id, "pending", "revocation_pending")
	n.converge(t)
	n.wantMember(t, revoked.id, "ready", "removed")
}

// A renewed enrollment clears the revocation, so the host can be told to remove what it holds and
// the network waits for that again. Revoking it again is the way out if it does not return.
func TestRenewedMemberWaitsForItsHostUntilRevokedAgain(t *testing.T) {
	n := newAheadNetwork(t, 3)
	revoked := n.hosts[2]
	n.save(t, n.hosts...)
	n.converge(t)
	n.revoke(t, revoked)
	n.converge(t)
	n.b.call("POST", n.base+"/servers/"+revoked.id.String()+"/enrollment", nil, 201, nil)
	n.wantMember(t, revoked.id, "pending", "revocation_pending")
	if _, _, note := n.revokedMember(t, revoked.id); !strings.Contains(note, "has not confirmed its removal") {
		t.Fatalf("renewed note = %q", note)
	}
	n.b.call("POST", n.base+"/servers/"+revoked.id.String()+"/revoke", nil, 204, nil)
	n.wantMember(t, revoked.id, "ready", "removed")
}

// A member the owner removed but did not revoke can still answer, so the network waits for it.
func TestRemovedMemberHoldsTheNetworkUntilItAnswersOrIsRevoked(t *testing.T) {
	for name, finish := range map[string]func(*testing.T, *aheadNetwork, *aheadHost){
		"it answers": func(t *testing.T, n *aheadNetwork, removed *aheadHost) {
			removed.silent = false
			n.converge(t)
			if !removed.held.Disabled {
				t.Fatal("the network is ready before the removed member gave up its links")
			}
		},
		"it is revoked": func(t *testing.T, n *aheadNetwork, removed *aheadHost) {
			n.b.call("POST", n.base+"/servers/"+removed.id.String()+"/revoke", nil, 204, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			n := newAheadNetwork(t, 3)
			a, b, removed := n.hosts[0], n.hosts[1], n.hosts[2]
			n.save(t, n.hosts...)
			n.converge(t)
			removed.silent = true
			n.save(t, a, b)
			n.until(t, func() bool { return !holdsPeer(a, removed.id) && !holdsPeer(b, removed.id) })
			for range 3 {
				n.round(t)
			}
			n.wantMember(t, removed.id, "pending", "revocation_pending")
			finish(t, n, removed)
			n.wantMember(t, removed.id, "ready", "removed")
		})
	}
}

// An archive carries the revocation with it. The destination's renewed approval goes to the
// remaining hosts, and the revoked one stays out without anything waiting on it.
func TestImportedRevokedMemberWaitsOnlyForTheRemainingHosts(t *testing.T) {
	n := newAheadNetwork(t, 3)
	a, b, revoked := n.hosts[0], n.hosts[1], n.hosts[2]
	n.save(t, n.hosts...)
	n.converge(t)
	n.revoke(t, revoked)
	n.converge(t)
	const passphrase = "revoked member archive test passphrase"
	var archive api.WorkspaceArchive
	n.b.call("POST", n.base+"/export", map[string]string{"passphrase": passphrase}, 200, &archive)
	if err := n.f.execution.Stop(n.ctx); err != nil {
		t.Fatal(err)
	}

	n.f = setup(t)
	n.b = n.f.browser(t)
	n.b.login("owner@example.test", n.f.password)
	n.b.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": archive.Archive, "passphrase": passphrase}, 201, nil)
	n.beat = time.Time{}
	n.wantMember(t, revoked.id, "pending", "revocation_pending")
	if status := claimCounter(t, n.f, `SELECT count(*) FROM servers WHERE id=$1 AND revoked`, revoked.id); status != 1 {
		t.Fatal("import dropped the revocation")
	}
	n.transfer(t, a)
	n.transfer(t, b)
	n.save(t, a, b)
	n.wantMember(t, revoked.id, "pending", "revocation_pending")
	n.converge(t)
	n.wantMember(t, revoked.id, "ready", "removed")
	if status := claimStatus(t, n.f, "/agent/v1/heartbeat", revoked.token, api.AgentHeartbeat{Capabilities: revoked.input.Capabilities, Preflight: revoked.input.Preflight}); status != http.StatusUnauthorized {
		t.Fatalf("revoked credential heartbeat after import = %d", status)
	}
}

// A restored backup and its fencing leave a revoked host revoked, and the owner's renewed approval
// goes through without it.
func TestRestoredRevokedMemberWaitsOnlyForTheRemainingHosts(t *testing.T) {
	n := newAheadNetwork(t, 3)
	a, b, revoked := n.hosts[0], n.hosts[1], n.hosts[2]
	n.save(t, n.hosts...)
	n.converge(t)
	n.revoke(t, revoked)
	n.converge(t)
	n.takeBackup(t)
	n.restore(t)
	if status := claimCounter(t, n.f, `SELECT count(*) FROM servers WHERE id=$1 AND revoked`, revoked.id); status != 1 {
		t.Fatal("recovery dropped the revocation")
	}
	// The backup's acknowledgements say nothing about what the hosts have held since it was taken.
	n.wantMember(t, revoked.id, "pending", "revocation_pending")
	n.wantMember(t, a.id, "pending", "pending")
	n.save(t, a, b)
	n.wantMember(t, revoked.id, "pending", "revocation_pending")
	n.converge(t)
	n.wantMember(t, revoked.id, "ready", "removed")
}

// revokedRecovery is a recovery whose capsule records a third, healthy member that has since been
// revoked and cannot export anything.
func revokedRecovery(t *testing.T) (*claimRecovery, api.AgentEnrollment) {
	t.Helper()
	var third api.AgentEnrollment
	r := newClaimRecovery(t, 1, func(f *fixture, b *browser, ws, network uuid.UUID) {
		_, third, _ = enrollment(t, f, b, ws, "deployment")
		if _, err := f.execution.Enroll(context.Background(), third); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.Pool.Exec(context.Background(), `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,enabled,approved_controller,generation) VALUES($1,$2,$3,$4,'10.249.0.3','192.0.2.3:51820',51820,1000,1024,$5,$6,true,0,1)`, ws, network, third.ServerId, uuid.New(), strings.Repeat("a", 64), claimKey("c")); err != nil {
			t.Fatal(err)
		}
	})
	return r, third
}

func (r *claimRecovery) revokeThird(t *testing.T, third api.AgentEnrollment) {
	t.Helper()
	for _, statement := range []string{
		`UPDATE servers SET revoked=true,credential_hash=NULL WHERE id=$1`,
		`UPDATE network_members SET enabled=false WHERE server_id=$1`,
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement, third.ServerId); err != nil {
			t.Fatal(err)
		}
	}
}

// removalOnThird is a standalone removal waiting on the third member.
func (r *claimRecovery) removalOnThird(t *testing.T, third api.AgentEnrollment, state string, sequence int64) api.ExecutionCommand {
	t.Helper()
	var membership uuid.UUID
	if err := r.f.st.Pool.QueryRow(r.ctx, `SELECT membership_id FROM network_members WHERE server_id=$1`, third.ServerId).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	removed := hostnetwork.Manifest{Version: 1, NetworkID: r.network, MembershipID: membership, ServerID: third.ServerId, Generation: 3, Prefix: "10.249.0.0/24", Address: "10.249.0.3", PublicKey: claimKey("c"), ListenPort: 51820}
	baseline := removed
	baseline.Generation = 2
	command := hostnetwork.Command{Action: "restrict", NetworkID: r.network, MembershipID: membership, Manifest: &removed, Baseline: &baseline}
	header := api.ExecutionCommand{Id: uuid.New(), ServerId: third.ServerId, ResourceId: r.network, Kind: "network", Action: "inspect", Generation: 1, PlanHash: hostnetwork.Hash(command), Deadline: time.Now().Add(time.Hour), Network: claimMessage(t, command)}
	if _, err := r.f.st.Pool.Exec(r.ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state,report_sequence) VALUES($1,$2,$3,1,$4,$5,'network',$6,$7)`, header.Id, r.ws, third.ServerId, uuid.New(), claimBytes(t, header), state, sequence); err != nil {
		t.Fatal(err)
	}
	return header
}

// thirdExport writes an export for the third member, as a host holding these proofs would.
func thirdExport(t *testing.T, third api.AgentEnrollment, machine uuid.UUID, proofs []byte) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "third")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(proofs)
	evidence := deployment.RecoveryEvidence{Version: 1, ServerID: third.ServerId, MachineID: machine, Units: map[string]string{}, Schedules: map[string]int64{}, Roots: []deployment.RecoveryRoot{}, ProofDigest: hex.EncodeToString(digest[:])}
	if err := os.WriteFile(filepath.Join(dir, "host.json"), claimBytes(t, evidence), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proofs.jsonl"), proofs, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Offline recovery needs nothing from a revoked host, whatever it left behind, and keeps it out.
func TestRecoveryNeedsNothingFromARevokedServer(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *claimRecovery, api.AgentEnrollment) uuid.UUID{
		"unchanged since the capsule": func(*testing.T, *claimRecovery, api.AgentEnrollment) uuid.UUID { return uuid.Nil },
		"re-added since the capsule": func(t *testing.T, r *claimRecovery, third api.AgentEnrollment) uuid.UUID {
			if _, err := r.f.st.Pool.Exec(r.ctx, `UPDATE network_members SET membership_id=$2,public_key=$3 WHERE server_id=$1`, third.ServerId, uuid.New(), claimKey("d")); err != nil {
				t.Fatal(err)
			}
			return uuid.Nil
		},
		"reported another key since the capsule": func(t *testing.T, r *claimRecovery, third api.AgentEnrollment) uuid.UUID {
			if _, err := r.f.st.Pool.Exec(r.ctx, `UPDATE network_members SET public_key=$2 WHERE server_id=$1`, third.ServerId, claimKey("d")); err != nil {
				t.Fatal(err)
			}
			return uuid.Nil
		},
		"known as a member only to the capsule": func(t *testing.T, r *claimRecovery, third api.AgentEnrollment) uuid.UUID {
			if _, err := r.f.st.Pool.Exec(r.ctx, `DELETE FROM network_members WHERE server_id=$1`, third.ServerId); err != nil {
				t.Fatal(err)
			}
			return uuid.Nil
		},
		"stalled on a removal it reported starting": func(t *testing.T, r *claimRecovery, third api.AgentEnrollment) uuid.UUID {
			return r.removalOnThird(t, third, "running", 1).Id
		},
		"never sent a queued removal": func(t *testing.T, r *claimRecovery, third api.AgentEnrollment) uuid.UUID {
			return r.removalOnThird(t, third, "queued", 0).Id
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, third := revokedRecovery(t)
			// Until it is revoked, this server has to answer for itself.
			in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: r.exports(t, claimExport{})}
			if _, err := r.f.execution.RecoverCrossHost(r.ctx, in); err == nil || !strings.Contains(err.Error(), "machine evidence") {
				t.Fatalf("recovery without an enrolled server's export: %v", err)
			}
			r.revokeThird(t, third)
			removal := change(t, r, third)
			r.recoverAll(t, r.exports(t, claimExport{}))
			if count := claimCounter(t, r.f, `SELECT count(*) FROM servers s JOIN network_members m ON m.server_id=s.id WHERE s.id=$1 AND s.revoked AND s.credential_hash IS NULL AND NOT m.enabled`, third.ServerId); count != 1 {
				t.Fatal("recovery brought the revoked server back")
			}
			if removal != uuid.Nil {
				if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE id=$1 AND state='cancelled' AND job_id IS NULL`, removal); count != 1 {
					t.Fatal("recovery left a removal waiting on the revoked host")
				}
			}
			var view struct {
				State string `json:"state"`
			}
			r.b.call("GET", "/api/v1/workspaces/"+r.ws.String()+"/network", nil, 200, &view)
			if view.State != "pending" {
				t.Fatalf("network after recovery = %q, want pending until the owner approves it again", view.State)
			}
		})
	}
}

// An export has to be the host's own, even for a server recovery could do without.
func TestRecoveryRefusesAnotherMachinesExportForARevokedServer(t *testing.T) {
	r, third := revokedRecovery(t)
	r.revokeThird(t, third)
	in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: append(r.exports(t, claimExport{}), thirdExport(t, third, uuid.New(), nil))}
	if _, err := r.f.execution.RecoverCrossHost(r.ctx, in); err == nil || !strings.Contains(err.Error(), "machine evidence") {
		t.Fatalf("recovery with another machine's export: %v", err)
	}
}

// What a revoked host did report still counts when it supplies an export.
func TestRecoveryKeepsARevokedServersExportedOutcome(t *testing.T) {
	r, third := revokedRecovery(t)
	r.revokeThird(t, third)
	header := r.removalOnThird(t, third, "running", 1)
	report := api.AgentReport{Id: header.Id, Generation: header.Generation, PlanHash: header.PlanHash, Sequence: 2, State: "failed", Steps: []api.ExecutionStep{}}
	proof := append(claimBytes(t, deployment.RecoveryProof{ParentID: uuid.Nil, ID: header.Id, Header: claimBytes(t, header), Report: claimBytes(t, report), Checkpoint: "accepted"}), '\n')
	r.recoverAll(t, append(r.exports(t, claimExport{}), thirdExport(t, third, third.MachineId, proof)))
	if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE id=$1 AND state='failed'`, header.Id); count != 1 {
		t.Fatal("recovery replaced the outcome the host reported")
	}
}
