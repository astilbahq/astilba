package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// The agreed limits on what a host's word can do. One step past each is refused.
const (
	receiptMargin    = 128
	historyAllowance = 1 << 32
	exportAllowance  = 1 << 44
)

func claimStatus(t *testing.T, f *fixture, path, token string, body any) int {
	t.Helper()
	raw, err := stablejson.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest("POST", f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func claimMessage(t *testing.T, v any) *jsontext.Value {
	t.Helper()
	raw, err := stablejson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	message := jsontext.Value(raw)
	return &message
}

func claimBytes(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := stablejson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func claimCounter(t *testing.T, f *fixture, query string, args ...any) int64 {
	t.Helper()
	var value int64
	if err := f.st.Pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

type claimHost struct {
	id       uuid.UUID
	token    string
	identity execution.Identity
	host     hostnetwork.Host
	input    api.AgentEnrollment
	current  hostnetwork.Manifest
}

// A modified agent answers a real restriction command, delivered by the real worker, with a
// before-state it made up. Nothing but the agent's word supports the generation in it.
func TestAgentCannotSpendNetworkGeneration(t *testing.T) {
	for name, removed := range map[string]bool{"enabled member": false, "removed member": true} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			b := f.browser(t)
			ctx := context.Background()
			ws := b.login("owner@example.test", f.password).Workspaces[0].Id
			base := "/api/v1/workspaces/" + ws.String()
			hosts := []*claimHost{}
			members := []api.ManagedNetworkMemberInput{}
			for i := range 3 {
				enrolled, in, token := enrollment(t, f, b, ws, "deployment")
				yes := true
				in.Capabilities.CrossHost, in.Capabilities.MaintenanceContinuation, in.Capabilities.Visual = &yes, &yes, &yes
				in.Preflight.CrossHostInventory = &api.CrossHostInventory{CpuMillis: 4000, MemoryMiB: 8192, UnboundedInventoryHash: hostnetwork.StableInventory(nil)}
				agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
				identity, err := f.execution.Authenticate(ctx, enrolled.Server.Id, strings.SplitN(token, ".", 2)[1])
				if err != nil {
					t.Fatal(err)
				}
				hosts = append(hosts, &claimHost{id: enrolled.Server.Id, token: token, identity: identity, host: hostnetwork.Host{Dir: t.TempDir()}, input: in})
				members = append(members, api.ManagedNetworkMemberInput{ServerId: enrolled.Server.Id, Endpoint: fmt.Sprintf("192.0.2.%d:51820", i+1), ListenPort: 51820, CpuMillis: 2000, MemoryMiB: 4096, UnboundedInventoryHash: hostnetwork.StableInventory(nil)})
			}
			attacker := hosts[0]
			if removed {
				attacker = hosts[2]
			}
			beat := time.Now()
			// Runs one honest round for every host, and hands back the attacker's restriction when armed.
			round := func(armed bool) *api.ExecutionCommand {
				t.Helper()
				if time.Since(beat) > 10*time.Second {
					for _, h := range hosts {
						if _, err := f.execution.Heartbeat(ctx, h.identity, api.AgentHeartbeat{Capabilities: h.input.Capabilities, Preflight: h.input.Preflight}); err != nil {
							t.Fatal(err)
						}
					}
					beat = time.Now()
				}
				for _, h := range hosts {
					poll, done := context.WithTimeout(ctx, 250*time.Millisecond)
					c, err := f.execution.Poll(poll, h.identity)
					expired := poll.Err()
					done()
					if err != nil {
						if waitForFixturePollLease(ctx, err) || expired != nil {
							continue
						}
						t.Fatal(err)
					}
					if c == nil {
						continue
					}
					if armed && h == attacker {
						var command hostnetwork.Command
						if err = hostnetwork.Decode(*c.Network, &command); err != nil {
							t.Fatal(err)
						}
						if command.Action == "restrict" {
							return c
						}
					}
					receipt, err := fixtureNetworkReceipt(h.host, *c, &h.current, nil, 0)
					if err != nil {
						t.Fatal(err)
					}
					report := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}, Network: claimMessage(t, receipt)}
					if err = f.execution.Report(ctx, h.identity, report); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			var network struct {
				Generation int64  `json:"generation"`
				State      string `json:"state"`
			}
			b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", Members: members}, 200, &network)
			for until := time.Now().Add(70 * time.Second); network.State != "ready"; b.call("GET", base+"/network", nil, 200, &network) {
				if time.Now().After(until) {
					t.Fatalf("network never became ready: %+v", network)
				}
				round(false)
			}
			b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", BaseGeneration: network.Generation, Members: members[:2]}, 200, &network)
			var frame *api.ExecutionCommand
			var command hostnetwork.Command
			// Waits for the sweep's next restriction to the attacker.
			restriction := func() {
				t.Helper()
				frame = nil
				for until := time.Now().Add(70 * time.Second); frame == nil; frame = round(true) {
					if time.Now().After(until) {
						t.Fatal("the attacker never received a restriction")
					}
				}
				command = hostnetwork.Command{}
				if err := hostnetwork.Decode(*frame.Network, &command); err != nil {
					t.Fatal(err)
				}
			}
			report := func(claimed int64) api.AgentReport {
				identity, err := attacker.host.Identity(command.MembershipID)
				if err != nil {
					t.Fatal(err)
				}
				forged := *command.Baseline
				forged.Generation, forged.Disabled = claimed, command.Manifest.Disabled
				forged.Peers, forged.Exports, forged.Egress = []hostnetwork.Peer{}, []hostnetwork.Endpoint{}, []hostnetwork.Egress{}
				forged.Sort()
				receipt := hostnetwork.Receipt{Identity: identity, NetworkID: command.NetworkID, Before: &forged, Effective: &forged, Generation: forged.Generation, Hash: hostnetwork.Hash(forged), Disabled: forged.Disabled}
				return api.AgentReport{Id: frame.Id, Generation: frame.Generation, PlanHash: frame.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}, Network: claimMessage(t, receipt)}
			}
			counter := func() int64 {
				return claimCounter(t, f, `SELECT generation FROM workspace_networks WHERE workspace_id=$1`, ws)
			}
			issued := counter()
			for _, claim := range []func() int64{func() int64 { return math.MaxInt64 - 1 }, func() int64 { return issued + historyAllowance }, func() int64 { return issued + receiptMargin + 1 }} {
				claimed := claim()
				restriction()
				// The report stands, because refusing it would also stop an honest host.
				if status := claimStatus(t, f, "/agent/v1/report", attacker.token, report(claimed)); status != http.StatusNoContent {
					t.Fatalf("claim %d: report status = %d", claimed, status)
				}
				if got := counter(); got != issued {
					t.Fatalf("claim %d moved the counter from %d to %d", claimed, issued, got)
				}
				var state, note string
				if err := f.st.Pool.QueryRow(ctx, `SELECT o.state,m.note FROM execution_operations o JOIN network_members m ON m.server_id=o.server_id WHERE o.id=$1`, frame.Id).Scan(&state, &note); err != nil {
					t.Fatal(err)
				}
				if state != "succeeded" || !strings.Contains(note, "far beyond") {
					t.Fatalf("claim %d: state = %q, member note = %q", claimed, state, note)
				}
				// It would answer the same again, so nothing more goes to it until something changes.
				sent := claimCounter(t, f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network'`, attacker.id)
				for until := time.Now().Add(6 * time.Second); time.Now().Before(until); {
					round(false)
				}
				if got := claimCounter(t, f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network'`, attacker.id); got != sent {
					t.Fatalf("%d more network commands went to the server after its claim", got-sent)
				}
				b.call("GET", base+"/network", nil, 200, &network)
				b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", BaseGeneration: network.Generation, Members: members[:2]}, 200, &network)
				issued = counter()
			}
			// A host can be this far ahead through restrictions it has applied but not reported.
			ahead := issued + receiptMargin
			restriction()
			if status := claimStatus(t, f, "/agent/v1/report", attacker.token, report(ahead)); status != http.StatusNoContent {
				t.Fatalf("work in flight: report status = %d", status)
			}
			if got := counter(); got != ahead+1 {
				t.Fatalf("counter = %d, want %d", got, ahead+1)
			}
			if got := claimCounter(t, f, `SELECT max(generation) FROM network_members WHERE workspace_id=$1`, ws); got != ahead+1 {
				t.Fatalf("members were not moved past the host: %d", got)
			}
			// Import refuses counters above portability.MaxCounter, so an inflated counter
			// would have left this workspace with an export nothing can import.
			var encrypted api.WorkspaceArchive
			const passphrase = "unique generation claims archive test passphrase"
			b.call("POST", base+"/export", map[string]string{"passphrase": passphrase}, 200, &encrypted)
			destination := setup(t)
			owner := destination.browser(t)
			owner.login("owner@example.test", destination.password)
			owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": encrypted.Archive, "passphrase": passphrase}, 201, nil)
			// Nothing the attacker said stands between the owner and the network or revocation.
			b.call("GET", base+"/network", nil, 200, &network)
			b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", BaseGeneration: network.Generation, Members: []api.ManagedNetworkMemberInput{}}, 200, &network)
			b.call("POST", base+"/servers/"+attacker.id.String()+"/enrollment", nil, 201, nil)
			b.call("POST", base+"/servers/"+attacker.id.String()+"/revoke", nil, 204, nil)
			if status := claimStatus(t, f, "/agent/v1/heartbeat", attacker.token, api.AgentHeartbeat{Capabilities: attacker.input.Capabilities, Preflight: attacker.input.Preflight}); status != http.StatusUnauthorized {
				t.Fatalf("revoked credential heartbeat = %d", status)
			}
		})
	}
}

func claimKey(c string) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(c, 32)))
}

// A counter that already reached the bigint limit, from before these bounds or from a restored
// capsule, must not stand between the owner and a revocation.
func TestRevocationSurvivesSpentGeneration(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ctx := context.Background()
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	base := "/api/v1/workspaces/" + ws.String()
	env := envForExecution(t, f, b, ws)
	type member struct {
		id, membership uuid.UUID
		token          string
		input          api.AgentEnrollment
		identity       execution.Identity
		address, key   string
	}
	const limit = int64(math.MaxInt64)
	network := uuid.New()
	members := []*member{}
	for i, key := range []string{"a", "b", "c", "d"} {
		enrolled, in, token := enrollment(t, f, b, ws, "deployment")
		yes := true
		in.Capabilities.CrossHost, in.Capabilities.MaintenanceContinuation, in.Capabilities.Visual = &yes, &yes, &yes
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
		identity, err := f.execution.Authenticate(ctx, enrolled.Server.Id, strings.SplitN(token, ".", 2)[1])
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, &member{id: enrolled.Server.Id, membership: uuid.New(), token: token, input: in, identity: identity, address: fmt.Sprintf("10.249.0.%d", i+1), key: claimKey(key)})
	}
	revoked, holding, behind, renewed := members[0], members[1], members[2], members[3]
	manifest := func(m *member, generation int64) hostnetwork.Manifest {
		out := hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: m.membership, ServerID: m.id, Generation: generation, Prefix: "10.249.0.0/24", Address: m.address, PublicKey: m.key, ListenPort: 51820, Peers: []hostnetwork.Peer{}, Exports: []hostnetwork.Endpoint{}, Egress: []hostnetwork.Egress{}}
		for _, peer := range members {
			if peer != m {
				out.Peers = append(out.Peers, hostnetwork.Peer{ServerID: peer.id, MembershipID: peer.membership, Address: peer.address, PublicKey: peer.key, Endpoint: "192.0.2.1:51820"})
			}
		}
		out.Sort()
		return out
	}
	// Revocation has no business with the server's and environment's counters either.
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspace_networks(id,workspace_id,prefix,generation) VALUES($1,$2,'10.249.0.0/24',$3)`, network, ws, limit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE servers SET update_generation=$2 WHERE id=$1`, revoked.id, limit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE environments SET execution_generation=$2 WHERE id=$1`, env, limit); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		installed := limit
		if m == behind {
			installed = 5
		}
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,enabled,approved_controller,generation,acknowledged_generation,installed_manifest) VALUES($1,$2,$3,$4,$5,'192.0.2.1:51820',51820,100,64,$6,$7,true,0,$8,$9,$10)`, ws, network, m.id, m.membership, m.address, strings.Repeat("a", 64), m.key, limit, limit, claimBytes(t, manifest(m, installed))); err != nil {
			t.Fatal(err)
		}
	}
	b.call("POST", base+"/servers/"+revoked.id.String()+"/revoke", nil, 204, nil)
	if status := claimStatus(t, f, "/agent/v1/heartbeat", revoked.token, api.AgentHeartbeat{Capabilities: revoked.input.Capabilities, Preflight: revoked.input.Preflight}); status != http.StatusUnauthorized {
		t.Fatalf("revoked credential heartbeat = %d", status)
	}
	var enabled bool
	if err := f.st.Pool.QueryRow(ctx, `SELECT enabled FROM network_members WHERE server_id=$1`, revoked.id).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("revocation left the membership enabled")
	}
	if got := claimCounter(t, f, `SELECT generation FROM workspace_networks WHERE workspace_id=$1`, ws); got != limit {
		t.Fatalf("spent counter changed to %d", got)
	}
	// The peers still hold the revoked key, and the owner can see that.
	var view struct {
		State   string `json:"state"`
		Members []struct {
			ServerID uuid.UUID `json:"serverId"`
			State    string    `json:"state"`
			Note     string    `json:"note"`
		} `json:"members"`
	}
	b.call("GET", base+"/network", nil, 200, &view)
	if view.State != "pending" {
		t.Fatalf("network state = %q, want pending while peers hold the revoked key", view.State)
	}
	for _, m := range view.Members {
		if m.ServerID == revoked.id {
			if m.State != "revocation_pending" {
				t.Fatalf("revoked member state = %q", m.State)
			}
			continue
		}
		if m.State != "pending" || !strings.Contains(m.Note, "reached its limit") {
			t.Fatalf("peer %+v does not say why it is pending", m)
		}
	}
	// Renewal runs the same invalidation, and the owner's other routes refuse instead of failing.
	b.call("POST", base+"/servers/"+renewed.id.String()+"/enrollment", nil, 201, nil)
	if status := (<-b.send("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.249.0.0/24", BaseGeneration: limit, Members: []api.ManagedNetworkMemberInput{}})).status; status != http.StatusBadRequest {
		t.Fatalf("PUT /network at the limit = %d", status)
	}
	// A peer that has not installed this generation can still be told. One that has cannot, and
	// the sweep leaves it pending rather than sending it work it must refuse on every pass.
	for _, m := range []*member{holding, behind} {
		if _, err := f.execution.Heartbeat(ctx, m.identity, api.AgentHeartbeat{Capabilities: m.input.Capabilities, Preflight: m.input.Preflight}); err != nil {
			t.Fatal(err)
		}
	}
	var delivered *api.ExecutionCommand
	for until := time.Now().Add(30 * time.Second); delivered == nil; {
		if time.Now().After(until) {
			t.Fatal("the peer behind the limit never received the removal")
		}
		poll, done := context.WithTimeout(ctx, 500*time.Millisecond)
		c, err := f.execution.Poll(poll, behind.identity)
		expired := poll.Err()
		done()
		if err != nil && !waitForFixturePollLease(ctx, err) && expired == nil {
			t.Fatal(err)
		}
		delivered = c
	}
	var command hostnetwork.Command
	if err := hostnetwork.Decode(*delivered.Network, &command); err != nil {
		t.Fatal(err)
	}
	if command.Manifest.Generation != limit {
		t.Fatalf("delivered generation = %d", command.Manifest.Generation)
	}
	for _, peer := range command.Manifest.Peers {
		if peer.ServerID == revoked.id {
			t.Fatal("the delivered manifest still admits the revoked server")
		}
	}
	identity := hostnetwork.Identity{MembershipID: behind.membership, PublicKey: behind.key}
	honest := hostnetwork.Receipt{Identity: identity, NetworkID: network, Generation: limit, Hash: hostnetwork.Hash(*command.Manifest), Disabled: command.Manifest.Disabled}
	if err := f.execution.Report(ctx, behind.identity, api.AgentReport{Id: delivered.Id, Generation: delivered.Generation, PlanHash: delivered.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}, Network: claimMessage(t, honest)}); err != nil {
		t.Fatal(err)
	}
	if got := claimCounter(t, f, `SELECT acknowledged_generation FROM network_members WHERE server_id=$1`, behind.id); got != limit {
		t.Fatalf("the removal was not acknowledged: %d", got)
	}
	if got := claimCounter(t, f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network'`, holding.id); got != 0 {
		t.Fatalf("%d network commands went to a peer that must refuse them", got)
	}
	if got := claimCounter(t, f, `SELECT count(*) FROM workspace_networks WHERE workspace_id=$1 AND reconcile_failing_since IS NOT NULL`, ws); got != 0 {
		t.Fatal("the network sweep is failing on the spent counters")
	}
}

// A host holding more than its in-flight work explains is one a restored management has forgotten.
// That can only be true once per controller generation, or a forged receipt could repeat it on
// every sweep and carry honest hosts beyond what the next restore accepts from them.
func TestReceiptHistoryAllowancePerController(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	_, in, _ := enrollment(t, f, b, ws, "deployment")
	if _, err := f.execution.Enroll(ctx, in); err != nil {
		t.Fatal(err)
	}
	identity, err := f.execution.Authenticate(ctx, in.ServerId, in.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	network, member := uuid.New(), uuid.New()
	key := claimKey("a")
	own := hostnetwork.Identity{MembershipID: member, PublicKey: key}
	manifest := func(generation int64, controller int64) hostnetwork.Manifest {
		return hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: member, ServerID: in.ServerId, ControllerGeneration: controller, Generation: generation, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: key, ListenPort: 51820}
	}
	const start = int64(5)
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO workspace_networks(id,workspace_id,prefix,generation) VALUES($1,$2,'10.251.0.0/24',$3)`, network, ws, start); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,approved_controller,generation,acknowledged_generation,installed_manifest) VALUES($1,$2,$3,$4,'10.251.0.1','192.0.2.1:51820',51820,1000,1024,$5,$6,0,$7,0,$8)`, ws, network, in.ServerId, member, hostnetwork.StableInventory(nil), key, start, claimBytes(t, manifest(start, 0))); err != nil {
		t.Fatal(err)
	}
	counter := func() int64 {
		return claimCounter(t, f, `SELECT generation FROM workspace_networks WHERE workspace_id=$1`, ws)
	}
	serial := int64(0)
	// Delivers one restriction frozen at this generation, answers it with a claim, and returns
	// whether the member was left waiting with the note.
	report := func(controller, frozen, claimed int64) bool {
		t.Helper()
		serial++
		baseline := manifest(frozen-1, 0)
		wanted := manifest(frozen, 0)
		body := hostnetwork.Command{Action: "restrict", NetworkID: network, MembershipID: member, Baseline: &baseline, Manifest: &wanted}
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: in.ServerId, Generation: serial, Kind: "network", Action: "inspect", ResourceId: network, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(time.Minute), Network: claimMessage(t, body)}
		if controller > 0 {
			c.ControllerGeneration = &controller
		}
		if _, e := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,'network','running')`, c.Id, ws, c.ServerId, c.Generation, uuid.New(), claimBytes(t, c)); e != nil {
			t.Fatal(e)
		}
		if _, e := f.st.Pool.Exec(ctx, `UPDATE servers SET controller_generation=$2 WHERE id=$1`, in.ServerId, controller); e != nil {
			t.Fatal(e)
		}
		if _, e := f.st.Pool.Exec(ctx, `UPDATE network_members SET note='' WHERE server_id=$1`, in.ServerId); e != nil {
			t.Fatal(e)
		}
		forged := manifest(claimed, controller)
		receipt := hostnetwork.Receipt{Identity: own, NetworkID: network, Before: &forged, Effective: &forged, Generation: claimed, Hash: hostnetwork.Hash(forged)}
		if e := f.execution.Report(ctx, identity, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}, Network: claimMessage(t, receipt)}); e != nil {
			t.Fatal(e)
		}
		var note string
		if e := f.st.Pool.QueryRow(ctx, `SELECT note FROM network_members WHERE server_id=$1`, in.ServerId).Scan(&note); e != nil {
			t.Fatal(e)
		}
		return strings.Contains(note, "far beyond")
	}
	claim := func(controller, claimed int64) bool { return report(controller, counter(), claimed) }
	// Management has forgotten nothing yet, so the allowance is not available at all.
	if !claim(0, start+receiptMargin+1) {
		t.Fatal("a claim past the margin was accounted for before any restore")
	}
	if !claim(1, start+historyAllowance+1) {
		t.Fatal("a claim past the allowance was accounted for")
	}
	if got := counter(); got != start {
		t.Fatalf("unaccounted claims moved the counter to %d", got)
	}
	// A command the owner has since moved past must not make the member wait, or a removal
	// issued after it would never be sent.
	if report(0, start-1, start+receiptMargin+1) {
		t.Fatal("a claim on a superseded command left the member waiting")
	}
	if got := counter(); got != start {
		t.Fatalf("an unaccounted claim on a superseded command moved the counter to %d", got)
	}
	if claim(1, start+historyAllowance) {
		t.Fatal("the first claim after a restore was not accounted for")
	}
	spent := counter()
	if spent != start+historyAllowance+1 {
		t.Fatalf("counter = %d", spent)
	}
	if !claim(1, spent+receiptMargin+1) {
		t.Fatal("a second large claim under the same controller generation was accounted for")
	}
	if got := counter(); got != spent {
		t.Fatalf("an unaccounted claim moved the counter to %d", got)
	}
	if claim(1, spent+receiptMargin) {
		t.Fatal("a claim within the margin was not accounted for")
	}
	spent = counter()
	if claim(2, spent+receiptMargin+1) {
		t.Fatal("the next restore's allowance was not available")
	}
}
