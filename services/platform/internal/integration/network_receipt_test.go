package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type networkRemovalJob struct{ ID uuid.UUID }

func (networkRemovalJob) Kind() string { return "platform_execution_v1" }

func TestManagedNetworkLostConfigureReceiptDoesNotBlockRemoval(t *testing.T) {
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
	host := hostnetwork.Host{Dir: t.TempDir()}
	member := uuid.New()
	own, err := host.Identity(member)
	if err != nil {
		t.Fatal(err)
	}
	peers := []hostnetwork.Peer{}
	for i := 0; i < 2; i++ {
		membership := uuid.New()
		key, err := host.Identity(membership)
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, hostnetwork.Peer{ServerID: uuid.New(), MembershipID: membership, PublicKey: key.PublicKey, Address: []string{"10.251.0.2", "10.251.0.3"}[i], Endpoint: []string{"192.0.2.2:51820", "192.0.2.3:51820"}[i]})
	}
	known := hostnetwork.Manifest{Version: 1, NetworkID: uuid.New(), MembershipID: member, ServerID: in.ServerId, Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: own.PublicKey, ListenPort: 51820, Peers: peers[:1]}
	configured := known
	configured.Generation, configured.Peers = 2, append([]hostnetwork.Peer{}, peers...)
	configured.Sort()
	wanted := known
	wanted.Generation, wanted.Peers = 3, nil
	effective, err := hostnetwork.ReconcileRestriction(configured, known, wanted, uuid.Nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO workspace_networks(id,workspace_id,prefix,generation) VALUES($1,$2,'10.251.0.0/24',3)`, known.NetworkID, ws); err != nil {
		t.Fatal(err)
	}
	knownRaw, _ := json.Marshal(known)
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,approved_controller,generation,acknowledged_generation,installed_manifest) VALUES($1,$2,$3,$4,'10.251.0.1','192.0.2.1:51820',51820,1000,1024,$5,$6,0,3,1,$7)`, ws, known.NetworkID, in.ServerId, member, hostnetwork.StableInventory(nil), own.PublicKey, knownRaw); err != nil {
		t.Fatal(err)
	}
	commands := []hostnetwork.Command{{Action: "restrict", NetworkID: known.NetworkID, MembershipID: member, Baseline: &known, Manifest: &wanted}, {Action: "configure", NetworkID: known.NetworkID, MembershipID: member, Manifest: &configured}}
	var frames []api.ExecutionCommand
	for i, body := range commands {
		raw, _ := json.Marshal(body)
		message := json.RawMessage(raw)
		id := []uuid.UUID{uuid.MustParse("00000000-0000-4000-8000-000000000001"), uuid.MustParse("00000000-0000-4000-8000-000000000002")}[i]
		c := api.ExecutionCommand{Id: id, ServerId: in.ServerId, Generation: int64(3 - i), Kind: "network", Action: "inspect", ResourceId: known.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(time.Minute), Network: &message}
		header, _ := json.Marshal(c)
		if _, err = f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,'network','running')`, c.Id, ws, c.ServerId, c.Generation, uuid.New(), header); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, c)
	}
	// The removal sorts first in the host's report queue. Its before-state
	// includes an approved configure whose receipt has not arrived yet.
	for i, c := range frames {
		observed := effective
		receipt := hostnetwork.Receipt{Identity: own, NetworkID: known.NetworkID}
		if i == 0 {
			receipt.Before, receipt.Effective = &configured, &effective
		} else {
			observed = configured
		}
		receipt.Generation, receipt.Hash, receipt.Disabled = observed.Generation, hostnetwork.Hash(observed), observed.Disabled
		raw, _ := json.Marshal(receipt)
		message := json.RawMessage(raw)
		r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Network: &message}
		if err = f.execution.Report(ctx, identity, r); err != nil {
			t.Fatal("valid out-of-order receipt blocked acknowledgement", i, err)
		}
		if err = f.execution.Report(ctx, identity, r); err != nil {
			t.Fatal("lost-response receipt retry was not idempotent", err)
		}
	}
	var installed []byte
	var pending bool
	if err = f.st.Pool.QueryRow(ctx, `SELECT installed_manifest,acknowledged_generation<generation FROM network_members WHERE server_id=$1`, in.ServerId).Scan(&installed, &pending); err != nil {
		t.Fatal(err)
	}
	var actual hostnetwork.Manifest
	if json.Unmarshal(installed, &actual) != nil || hostnetwork.Hash(actual) != hostnetwork.Hash(effective) || !pending {
		t.Fatal("late configure receipt rewound actual authority or falsely claimed convergence")
	}
	t.Run("queued_removals_dispatch_serially", func(t *testing.T) {
		current, err := execution.New(f.st)
		if err != nil {
			t.Fatal(err)
		}
		var ids []uuid.UUID
		for i := 0; i < 2; i++ {
			c := frames[0]
			c.Id, c.Generation = uuid.New(), int64(10+i)
			raw, _ := json.Marshal(c)
			if _, err = f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,'network','queued')`, c.Id, ws, c.ServerId, c.Generation, uuid.New(), raw); err != nil {
				t.Fatal("queued removal backlog could not be retained", err)
			}
			if _, err = current.Queue.Insert(ctx, networkRemovalJob{ID: c.Id}, nil); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, c.Id)
		}
		if err = current.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer current.Stop(ctx)
		waitActive := func() api.ExecutionCommand {
			t.Helper()
			until := time.Now().Add(15 * time.Second)
			for time.Now().Before(until) {
				var raw []byte
				if err := f.st.Pool.QueryRow(ctx, `SELECT command FROM execution_operations WHERE id=ANY($1) AND state='dispatched'`, ids).Scan(&raw); err == nil {
					var c api.ExecutionCommand
					if json.Unmarshal(raw, &c) != nil {
						t.Fatal("invalid dispatched removal")
					}
					return c
				}
				time.Sleep(25 * time.Millisecond)
			}
			t.Fatal("queued removal never dispatched")
			return api.ExecutionCommand{}
		}
		first := waitActive()
		var queued uuid.UUID
		if err = f.st.Pool.QueryRow(ctx, `SELECT id FROM execution_operations WHERE id=ANY($1) AND state='queued'`, ids).Scan(&queued); err != nil {
			t.Fatal("a second removal dispatched concurrently", err)
		}
		_, err = f.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='running' WHERE id=$1`, queued)
		var unique *pgconn.PgError
		if !errors.As(err, &unique) || unique.Code != "23505" {
			t.Fatal("database no longer independently rejects two active removals", err)
		}
		complete := func(c api.ExecutionCommand) {
			t.Helper()
			receipt := hostnetwork.Receipt{Identity: own, NetworkID: known.NetworkID, Generation: effective.Generation, Hash: hostnetwork.Hash(effective), Before: &effective, Effective: &effective}
			raw, _ := json.Marshal(receipt)
			message := json.RawMessage(raw)
			if err := current.Report(ctx, identity, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Network: &message}); err != nil {
				t.Fatal(err)
			}
		}
		complete(first)
		second := waitActive()
		if second.Id != queued {
			t.Fatal("the waiting removal was replaced")
		}
		complete(second)
	})
}
