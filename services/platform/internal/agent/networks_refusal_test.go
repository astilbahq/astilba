package agent

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// fakeManagedHost answers for the kernel, WireGuard and nftables, which only a prepared Linux
// host has. Apply returns what the host's own checks would have.
type fakeManagedHost struct {
	key     string
	held    *hostnetwork.Manifest
	apply   error
	applied int
}

func (h *fakeManagedHost) Supported(context.Context) bool { return true }
func (h *fakeManagedHost) Identity(id uuid.UUID) (hostnetwork.Identity, error) {
	return hostnetwork.Identity{MembershipID: id, PublicKey: h.key}, nil
}
func (h *fakeManagedHost) Snapshot(id uuid.UUID) (hostnetwork.Snapshot, error) {
	if h.held == nil {
		return hostnetwork.Snapshot{}, &os.PathError{Op: "lstat", Path: id.String(), Err: os.ErrNotExist}
	}
	return hostnetwork.Snapshot{Manifest: *h.held}, nil
}
func (h *fakeManagedHost) Apply(context.Context, hostnetwork.Manifest, []hostnetwork.Bridge, bool) error {
	h.applied++
	return h.apply
}

// networkJoinFixture is a prepared host and a configure joining 10.251.0.0/24 with no
// exports or egress, which needs no Docker call of its own.
func networkJoinFixture(t *testing.T, docker *client.Client, parent *uuid.UUID) (*Engine, *fakeManagedHost, api.ExecutionCommand) {
	t.Helper()
	j, p := testVisualJournal(t)
	if err := os.WriteFile(filepath.Join(j.dir, "network-enabled"), []byte("astilba-managed-network-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	m := hostnetwork.Manifest{Version: 1, NetworkID: uuid.New(), MembershipID: uuid.New(), ServerID: p.ServerID, Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: key, ListenPort: 51820, Peers: []hostnetwork.Peer{}, Exports: []hostnetwork.Endpoint{}, Egress: []hostnetwork.Egress{}}
	body := hostnetwork.Command{Action: "configure", NetworkID: m.NetworkID, MembershipID: m.MembershipID, Manifest: &m}
	raw, _ := stablejson.Marshal(body)
	message := jsontext.Value(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: parent, ServerId: p.ServerID, Generation: 1, Kind: "network", Action: "inspect", ResourceId: m.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(time.Hour), Network: &message}
	host := &fakeManagedHost{key: key}
	return &Engine{Journal: j, Docker: docker, managed: host}, host, c
}

func runNetworkCommand(t *testing.T, e *Engine, c api.ExecutionCommand) (Record, error) {
	t.Helper()
	r, err := e.Journal.Accept(c)
	if err != nil {
		t.Fatal(err)
	}
	err = e.executeNetwork(t.Context(), c, r)
	r, x := e.Journal.Record(c.Id)
	if x != nil {
		t.Fatal(x)
	}
	return r, err
}

// A join over a range in use on the host changes nothing, so it must end: an unfinished record
// holds the host's other commands and controller transfer, and a coordinated parent would hold
// its deployment's reservations for a child the agent never reruns. Management that reads
// reasons is told this is no generation conflict, so it doesn't advise fencing a healthy host,
// and what the host holds, so it can still send removals. Older management is told what older
// agents told it. Until management answers, after the agent restarts, it isn't known which
// applies, and deciding wrongly would end the command for good.
func TestRouteConflictEndsTheJoinWithItsReason(t *testing.T) {
	parent := uuid.New()
	overlap := netip.MustParsePrefix("10.251.0.0/25")
	for _, tc := range []struct {
		name        string
		parent      *uuid.UUID
		reasons     int32
		state       api.ExecutionState
		receipt     bool
		remedy      string
		firstJoin   bool
		readded     bool
		withNetwork map[string]any
	}{
		{name: "standalone", reasons: refusalsRead, state: "failed", receipt: true, remedy: "save the network again"},
		// A host removed and added again holds what it was sent under its old membership,
		// which management would refuse as a description of the new one, for good.
		{name: "added again", reasons: refusalsRead, state: "failed", receipt: true, remedy: "save the network again", readded: true},
		{name: "older management", reasons: refusalsUnread, state: "needs_attention", remedy: "Remove that network or route"},
		{name: "management not yet heard from", reasons: refusalsUnknown, state: "running"},
		{name: "coordinated links", parent: &parent, state: "failed", remedy: "deploy again"},
		// Isolated gateway mode leaves a bridge without a route, as Docker not having started
		// yet does, so only Docker's record shows it.
		{name: "first join over an unrouted bridge", reasons: refusalsRead, state: "failed", receipt: true, remedy: "save the network again", firstJoin: true, withNetwork: isolatedBridge(overlap.String())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			networks := []map[string]any{}
			if tc.withNetwork != nil {
				networks = append(networks, tc.withNetwork)
			}
			e, host, c := networkJoinFixture(t, serveFakeNetworks(t, networks...), tc.parent)
			e.networkRefusals.Store(tc.reasons)
			var held hostnetwork.Manifest
			if !tc.firstJoin {
				var command hostnetwork.Command
				if err := hostnetwork.Decode(*c.Network, &command); err != nil {
					t.Fatal(err)
				}
				held = *command.Manifest
				if tc.readded {
					held.MembershipID = uuid.New()
				}
				held.Peers = []hostnetwork.Peer{{ServerID: uuid.New(), MembershipID: uuid.New(), Address: "10.251.0.9", PublicKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("p", 32))), Endpoint: "192.0.2.9:51820"}}
				host.held = &held
				host.apply = hostnetwork.RouteConflictError{Range: overlap}
			}
			r, err := runNetworkCommand(t, e, c)
			if tc.state == "running" {
				if err == nil || r.Report.State != "running" || r.Checkpoint != "network-apply" {
					t.Fatalf("decided before management answered: %v, %s: %s", err, r.Report.State, r.Report.Note)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Report.State != tc.state || !strings.Contains(r.Report.Note, overlap.String()) || !strings.Contains(r.Report.Note, tc.remedy) || strings.Contains(r.Report.Note, "prepare-recovery") {
				t.Fatalf("report %s: %s", r.Report.State, r.Report.Note)
			}
			if tc.firstJoin && host.applied != 0 {
				t.Fatal("the join was applied over a container network")
			}
			var receipt hostnetwork.Receipt
			if !tc.receipt {
				// Every management accepts a failure without a receipt, and a coordinated phase
				// has no member note to keep.
				if r.Report.Network != nil {
					t.Fatalf("receipt sent: %s", *r.Report.Network)
				}
				return
			}
			if r.Report.Network == nil || hostnetwork.Decode(*r.Report.Network, &receipt) != nil || !receipt.Refused || receipt.Reason != hostnetwork.RouteConflict {
				t.Fatalf("report = %+v", r.Report)
			}
			if (tc.firstJoin || tc.readded) != (receipt.Before == nil) || receipt.Before != nil && hostnetwork.Hash(*receipt.Before) != hostnetwork.Hash(held) {
				t.Fatalf("the receipt does not say what the host holds: %+v", receipt.Before)
			}
		})
	}
}

// isolatedBridge is an internal bridge in isolated gateway mode, which Docker lists with its
// subnet but gives no address or route on the host.
func isolatedBridge(subnet string) map[string]any {
	n := fakeNetwork("isolated", "bridge", subnet)
	n["Internal"] = true
	n["Options"] = map[string]string{"com.docker.network.bridge.gateway_mode_ipv4": "isolated"}
	return n
}

// Swarm-scoped networks, which may not be attached here, ipvlan in L3 mode, which the host
// doesn't route to, and config-only templates have no addresses on this host, so a host that
// joined beside one was right to. Counting them would refuse it its next configure, such as a
// new peer. Bridge, macvlan and ipvlan L2 networks have them, routed or not. A network made
// from a template is listed with the template's subnets and options, as Docker 29 lists it.
// A disabled manifest is how management removes a member it has no record of, and refusing it
// would leave that removal pending for good. A disabled network a removed member kept was not
// checked, so it doesn't spare the member's next join the check. An enabled one, kept because the
// member was added again before its removal landed, holds the prefix, and the join replaces it.
func TestFirstJoinCountsOnlyNetworksWithAddressesHere(t *testing.T) {
	ignored := []map[string]any{
		fakeNetwork("swarm-overlay", "overlay", "10.251.0.0/24"),
		fakeNetwork("swarm-macvlan", "macvlan", "10.251.0.0/25"),
		fakeNetwork("ipvlan-l3", "ipvlan", "10.251.0.64/26"),
		fakeNetwork("l3-template", "null", ""),
		fakeNetwork("from-l3-template", "ipvlan", "10.251.0.128/26"),
		fakeNetwork("host", "host", ""),
	}
	ignored[0]["Scope"] = "swarm"
	ignored[1]["Scope"] = "swarm"
	ignored[2]["Options"] = map[string]string{"ipvlan_mode": "l3s"}
	ignored[3]["ConfigOnly"] = true
	ignored[3]["IPAM"] = map[string]any{"Config": []map[string]string{{"Subnet": "10.251.0.128/26"}}}
	ignored[3]["Options"] = map[string]string{"ipvlan_mode": "l3"}
	ignored[4]["ConfigFrom"] = map[string]string{"Network": "l3-template"}
	ignored[4]["Options"] = map[string]string{"ipvlan_mode": "l3"}
	fromTemplate := fakeNetwork("from-l2-template", "macvlan", "10.251.0.192/26")
	fromTemplate["ConfigFrom"] = map[string]string{"Network": "l2-template"}
	for _, tc := range []struct {
		name     string
		extra    []map[string]any
		disabled bool
		kept     *hostnetwork.Manifest // what the host keeps; a nil membership is the command's
		overlap  string
	}{
		{name: "none here"},
		{name: "a removal", extra: []map[string]any{isolatedBridge("10.251.0.0/28")}, disabled: true},
		{name: "added again after a removal", extra: []map[string]any{isolatedBridge("10.251.0.0/28")}, kept: &hostnetwork.Manifest{MembershipID: uuid.New(), Disabled: true}, overlap: "10.251.0.0/28"},
		{name: "added again before its removal landed", extra: []map[string]any{isolatedBridge("10.251.0.0/28")}, kept: &hostnetwork.Manifest{MembershipID: uuid.New()}},
		{name: "removed under this membership", extra: []map[string]any{isolatedBridge("10.251.0.0/28")}, kept: &hostnetwork.Manifest{Disabled: true}, overlap: "10.251.0.0/28"},
		{name: "isolated bridge", extra: []map[string]any{isolatedBridge("10.251.0.0/28")}, overlap: "10.251.0.0/28"},
		{name: "macvlan", extra: []map[string]any{fakeNetwork("lan", "macvlan", "10.251.0.0/23")}, overlap: "10.251.0.0/23"},
		{name: "ipvlan in its default mode", extra: []map[string]any{fakeNetwork("ipvlan-l2", "ipvlan", "10.251.0.16/28")}, overlap: "10.251.0.16/28"},
		{name: "made from a config-only network", extra: []map[string]any{fakeNetwork("l2-template", "null", ""), fromTemplate}, overlap: "10.251.0.192/26"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, host, c := networkJoinFixture(t, serveFakeNetworks(t, append(append([]map[string]any{}, ignored...), tc.extra...)...), nil)
			var command hostnetwork.Command
			if err := hostnetwork.Decode(*c.Network, &command); err != nil {
				t.Fatal(err)
			}
			command.Manifest.Disabled = tc.disabled
			if tc.kept != nil {
				kept := *command.Manifest
				kept.Disabled = tc.kept.Disabled
				if tc.kept.MembershipID != uuid.Nil {
					kept.MembershipID = tc.kept.MembershipID
				}
				host.held = &kept
			}
			err := e.containerNetworkConflict(t.Context(), host, command)
			var conflict hostnetwork.RouteConflictError
			if tc.overlap == "" && err != nil || tc.overlap != "" && (!errors.As(err, &conflict) || conflict.Range.String() != tc.overlap) {
				t.Fatalf("conflict = %v, want %q", err, tc.overlap)
			}
		})
	}
}

// Docker may be down while the agent runs, after a reboot or during an update, and a join
// that has no exports or egress never needed it before. A join it already holds doesn't ask
// Docker at all. A first join that can't read Docker's record changed nothing, so it reruns
// from its checkpoint rather than end needing attention, which the agent never reruns.
func TestFirstJoinWaitsForDocker(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	docker, err := client.New(client.WithHost(down.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })

	e, host, c := networkJoinFixture(t, docker, nil)
	var command hostnetwork.Command
	if err = hostnetwork.Decode(*c.Network, &command); err != nil {
		t.Fatal(err)
	}
	host.held = command.Manifest
	if r, err := runNetworkCommand(t, e, c); err != nil || r.Report.State != "succeeded" {
		t.Fatalf("a held network asked Docker: %v, %s: %s", err, r.Report.State, r.Report.Note)
	}

	e, host, c = networkJoinFixture(t, docker, nil)
	r, err := runNetworkCommand(t, e, c)
	if err == nil || r.Report.State != "running" || r.Checkpoint != "network-apply" || host.applied != 0 {
		t.Fatalf("with Docker down: %v, %s at %s: %s", err, r.Report.State, r.Checkpoint, r.Report.Note)
	}
	e.Docker = serveFakeNetworks(t)
	if err = e.Execute(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if r, err = e.Journal.Record(c.Id); err != nil || r.Report.State != "succeeded" || host.applied != 1 {
		t.Fatalf("the rerun: %v, %s: %s", err, r.Report.State, r.Report.Note)
	}
}

// Management advertises that it reads refusal reasons on every response. A controller that
// stops advertising it, after a downgrade, is sent none, even in a refusal recorded while the
// one before it read them: it would refuse that report for good, and the operation would hold
// the host. It gets a plain failure instead, which every controller reads.
func TestRefusalReasonsFollowTheController(t *testing.T) {
	j, _ := testVisualJournal(t)
	advertised := true
	received := []bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report struct {
			Network *jsontext.Value `json:"network"`
		}
		_ = json.UnmarshalRead(r.Body, &report)
		received = append(received, report.Network != nil)
		w.Header().Set("Astilba-Agent-Features", "build-controls")
		if advertised {
			w.Header().Set("Astilba-Agent-Network-Refusals", "v1")
		}
	}))
	t.Cleanup(server.Close)
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin = server.URL
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Journal: j}
	c := NewClient(j, e)
	c.HTTP = server.Client()
	raw, _ := stablejson.Marshal(hostnetwork.Receipt{NetworkID: uuid.New(), Refused: true, Reason: hostnetwork.RouteConflict})
	refusal := jsontext.Value(raw)
	report := api.AgentReport{Id: uuid.New(), State: "failed", Network: &refusal}
	for _, step := range []struct {
		advertise bool
		state     int32
		sent      bool
	}{{true, refusalsRead, true}, {true, refusalsRead, true}, {false, refusalsUnread, true}, {false, refusalsUnread, false}} {
		advertised = step.advertise
		if err = c.call(t.Context(), "POST", "/agent/v1/report", report, nil, "fixture"); err != nil {
			t.Fatal(err)
		}
		if e.networkRefusals.Load() != step.state || received[len(received)-1] != step.sent {
			t.Fatalf("advertised %v: agent reads %d, sent the reason %v", step.advertise, e.networkRefusals.Load(), received[len(received)-1])
		}
	}
}

// A member removed before it ever joined holds no peers or grants, which is what its disabled
// manifest says. It acknowledges each removal as is, the first and every later one: applying
// could be refused over a route on the host, and a restriction can't be scoped against state the
// host doesn't have, and either would leave the network waiting on that removal for good. A host
// keeping an earlier membership's network applies its removal, which replaces that membership as
// recovery expects of its export, and a manifest that adds the host is always applied.
func TestRemovalOfNothingIsAcknowledgedAsIs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		action   string
		disabled bool
		kept     *bool // the earlier membership's network, disabled or not
		applied  int
	}{
		{name: "removed before joining", action: "configure", disabled: true},
		{name: "removed again before joining", action: "restrict", disabled: true},
		{name: "joining", action: "configure", applied: 1},
		{name: "removed after an earlier membership it was removed from", action: "configure", disabled: true, kept: new(true), applied: 1},
		{name: "removed while an earlier membership still holds peers", action: "configure", disabled: true, kept: new(false), applied: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, host, c := networkJoinFixture(t, serveFakeNetworks(t), nil)
			var command hostnetwork.Command
			if err := hostnetwork.Decode(*c.Network, &command); err != nil {
				t.Fatal(err)
			}
			command.Action = tc.action
			command.Manifest.Disabled = tc.disabled
			raw, _ := stablejson.Marshal(command)
			message := jsontext.Value(raw)
			c.Network, c.PlanHash = &message, hostnetwork.Hash(command)
			if tc.kept != nil {
				kept := *command.Manifest
				kept.MembershipID, kept.Disabled = uuid.New(), *tc.kept
				host.held = &kept
			}
			r, err := runNetworkCommand(t, e, c)
			var receipt hostnetwork.Receipt
			if err != nil || r.Report.State != "succeeded" || r.Report.Network == nil || hostnetwork.Decode(*r.Report.Network, &receipt) != nil {
				t.Fatalf("%v, %s: %s", err, r.Report.State, r.Report.Note)
			}
			if host.applied != tc.applied || receipt.Hash != hostnetwork.Hash(*command.Manifest) || receipt.Disabled != tc.disabled || receipt.Before != nil {
				t.Fatalf("applied %d times, receipt %+v", host.applied, receipt)
			}
		})
	}
}
