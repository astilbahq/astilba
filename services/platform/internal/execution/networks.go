package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math"
	"sort"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/astilbahq/astilba/services/platform/internal/sweep"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type managedNetwork struct {
	ID         uuid.UUID       `json:"id"`
	Generation int64           `json:"generation"`
	Prefix     string          `json:"prefix"`
	State      string          `json:"state"`
	Members    []networkMember `json:"members"`
}
type networkMember struct {
	ServerID      uuid.UUID `json:"serverId"`
	MembershipID  uuid.UUID `json:"membershipId"`
	Address       string    `json:"address"`
	Endpoint      string    `json:"endpoint"`
	ListenPort    int       `json:"listenPort"`
	CPUMillis     int       `json:"cpuMillis"`
	MemoryMiB     int       `json:"memoryMiB"`
	InventoryHash string    `json:"unboundedInventoryHash"`
	PublicKey     string    `json:"publicKey,omitempty"`
	Enabled       bool      `json:"enabled"`
	Generation    int64     `json:"generation"`
	Acknowledged  int64     `json:"acknowledgedGeneration"`
	State         string    `json:"state"`
	Note          string    `json:"note"`
	Controller    int64     `json:"-"`
	ManifestHash  string    `json:"-"`
	revoked       bool
}

func readNetwork(ctx context.Context, tx pgx.Tx, ws uuid.UUID) (managedNetwork, error) {
	n := managedNetwork{Members: []networkMember{}, State: "unconfigured"}
	err := tx.QueryRow(ctx, `SELECT id,generation,prefix::text FROM workspace_networks WHERE workspace_id=$1`, ws).Scan(&n.ID, &n.Generation, &n.Prefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	rows, err := tx.Query(ctx, `SELECT m.server_id,m.membership_id,host(m.address),m.endpoint,m.listen_port,m.cpu_millis,m.memory_mib,m.unbounded_inventory_hash,m.public_key,m.enabled,m.generation,m.acknowledged_generation,m.note,m.approved_controller,m.manifest_hash,s.revoked,s.controller_generation FROM network_members m JOIN servers s ON s.workspace_id=m.workspace_id AND s.id=m.server_id WHERE m.workspace_id=$1 ORDER BY m.server_id`, ws)
	if err != nil {
		return n, err
	}
	defer rows.Close()
	n.State = "ready"
	for rows.Next() {
		var m networkMember
		var controller int64
		if err = rows.Scan(&m.ServerID, &m.MembershipID, &m.Address, &m.Endpoint, &m.ListenPort, &m.CPUMillis, &m.MemoryMiB, &m.InventoryHash, &m.PublicKey, &m.Enabled, &m.Generation, &m.Acknowledged, &m.Note, &m.Controller, &m.ManifestHash, &m.revoked, &controller); err != nil {
			return n, err
		}
		// A restored or imported acknowledgement says what the host held under an earlier controller.
		// It may hold more now, so only the owner's renewed approval and a fresh answer count.
		approved := m.Controller == controller
		acknowledged := approved && m.Acknowledged == m.Generation
		m.State = "removed"
		// A revoked server can never answer, and what removes its access is its peers dropping its
		// key, so only they hold the network for it.
		if !m.Enabled && !m.revoked && m.PublicKey != "" && !acknowledged {
			m.State = "revocation_pending"
			n.State = "pending"
		}
		if m.Enabled {
			m.State = "pending"
			if acknowledged && m.PublicKey != "" {
				m.State = "ready"
			} else {
				n.State = "pending"
			}
			if !approved && m.Note == "" {
				m.Note = renewedNetworkApprovalNote
			}
		}
		n.Members = append(n.Members, m)
	}
	if err = rows.Err(); err != nil {
		return n, err
	}
	for i := range n.Members {
		if m := &n.Members[i]; !m.Enabled && m.revoked {
			m.Note = revokedMemberRemovedNote
			// Only a member's acknowledgement of the current generation proves what it holds: its
			// record can miss a peer that a lost receipt added. So while anything is pending, and
			// each new generation makes everything pending, this cannot claim the key is gone.
			if n.State == "pending" {
				m.State = "revocation_pending"
				m.Note = revokedMemberPendingNote
			}
		}
	}
	return n, nil
}
func (s *Service) ManagedNetwork(ctx context.Context, p store.Principal, ws uuid.UUID) (managedNetwork, error) {
	var n managedNetwork
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error { var e error; n, e = readNetwork(ctx, tx, ws); return e })
	return n, err
}
func (s *Service) ConfigureManagedNetwork(ctx context.Context, p store.Principal, ws uuid.UUID, in api.ConfigureManagedNetwork) (managedNetwork, error) {
	var out managedNetwork
	prefix, err := hostnetwork.Prefix(in.Prefix)
	if err != nil || len(in.Members) > hostnetwork.MaxMembers {
		return out, domain.Invalid("network")
	}
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "network.configure", ws, func(tx pgx.Tx) error {
		old, e := readNetwork(ctx, tx, ws)
		if e != nil {
			return e
		}
		if old.Generation != in.BaseGeneration || old.ID != uuid.Nil && old.Prefix != in.Prefix {
			return domain.Conflict
		}
		var busy bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cross_host_reservations WHERE workspace_id=$1)`, ws).Scan(&busy); e != nil {
			return e
		}
		if busy {
			return domain.Conflict
		}
		id := old.ID
		if id == uuid.Nil {
			id = uuid.New()
		}
		if old.Generation == math.MaxInt64 {
			return domain.Invalid("network.generation")
		}
		generation := old.Generation + 1
		prior := map[uuid.UUID]networkMember{}
		used := map[string]bool{}
		for _, m := range old.Members {
			prior[m.ServerID] = m
			used[m.Address] = true
		}
		seen := map[uuid.UUID]bool{}
		type admitted struct {
			in         api.ManagedNetworkMemberInput
			controller int64
			member     networkMember
		}
		members := []admitted{}
		sort.Slice(in.Members, func(i, j int) bool { return in.Members[i].ServerId.String() < in.Members[j].ServerId.String() })
		for _, v := range in.Members {
			if seen[v.ServerId] || !hostnetwork.TransportEndpoint(v.Endpoint) || v.ListenPort < 1024 || v.ListenPort > 65535 || v.CpuMillis < 100 || v.MemoryMiB < 64 || len(v.UnboundedInventoryHash) != 64 {
				return domain.Invalid("network.members")
			}
			seen[v.ServerId] = true
			var cap, pre []byte
			var controller int64
			var ready bool
			e = tx.QueryRow(ctx, `SELECT capabilities,preflight,controller_generation,NOT revoked AND NOT awaiting_transfer AND NOT preview_only AND role<>'build' AND last_seen>now()-interval '45 seconds' FROM servers WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, v.ServerId).Scan(&cap, &pre, &controller, &ready)
			if errors.Is(e, pgx.ErrNoRows) {
				return domain.NotFound
			}
			if e != nil {
				return e
			}
			var c api.AgentCapabilities
			var h api.HostPreflight
			if !ready || json.Unmarshal(cap, &c) != nil || c.CrossHost == nil || !*c.CrossHost || json.Unmarshal(pre, &h) != nil || h.CrossHostInventory == nil || h.CrossHostInventory.UnboundedInventoryHash != v.UnboundedInventoryHash || v.CpuMillis >= h.CrossHostInventory.CpuMillis || v.MemoryMiB >= h.CrossHostInventory.MemoryMiB {
				return domain.Invalid("network.capacity")
			}
			var cpu, mem int64
			if e = tx.QueryRow(ctx, `SELECT COALESCE(sum(cpu_millis),0),COALESCE(sum(memory_mib),0) FROM cross_host_runtime WHERE server_id=$1`, v.ServerId).Scan(&cpu, &mem); e != nil {
				return e
			}
			if cpu > int64(v.CpuMillis) || mem > int64(v.MemoryMiB) {
				return domain.Invalid("network.capacity")
			}
			m, exists := prior[v.ServerId]
			if !exists {
				addr := prefix.Addr().Next()
				for used[addr.String()] {
					addr = addr.Next()
				}
				if !prefix.Contains(addr) {
					return domain.Invalid("network.addresses")
				}
				m = networkMember{MembershipID: uuid.New(), Address: addr.String()}
				used[m.Address] = true
			}
			if !m.Enabled && exists {
				m.MembershipID = uuid.New()
				m.PublicKey = ""
				m.Acknowledged = 0
			}
			members = append(members, admitted{v, controller, m})
		}
		for _, m := range old.Members {
			if m.Enabled && !seen[m.ServerID] {
				var active bool
				if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cross_host_runtime WHERE server_id=$1)`, m.ServerID).Scan(&active); e != nil {
					return e
				}
				if active {
					return domain.Invalid("network.inUse")
				}
			}
		}
		if _, e = tx.Exec(ctx, `INSERT INTO workspace_networks(id,workspace_id,prefix,generation) VALUES($1,$2,$3,$4) ON CONFLICT(workspace_id) DO UPDATE SET generation=$4`, id, ws, in.Prefix, generation); e != nil {
			return e
		}
		// Removing a member is approval for its current controller to tell it. Without this, a
		// member removed by the first save after a restore would keep what it holds.
		if _, e = tx.Exec(ctx, `UPDATE network_members m SET enabled=false,generation=$2,note='',approved_controller=s.controller_generation FROM servers s WHERE m.workspace_id=$1 AND s.id=m.server_id`, ws, generation); e != nil {
			return e
		}
		for _, a := range members {
			v, m := a.in, a.member
			if _, e = tx.Exec(ctx, `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,enabled,approved_controller,generation) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,true,$12,$13) ON CONFLICT(workspace_id,server_id) DO UPDATE SET membership_id=$4,address=$5,endpoint=$6,listen_port=$7,cpu_millis=$8,memory_mib=$9,unbounded_inventory_hash=$10,public_key=$11,enabled=true,approved_controller=$12,generation=$13,note='',acknowledged_generation=0,manifest_hash=''`, ws, id, v.ServerId, m.MembershipID, m.Address, v.Endpoint, v.ListenPort, v.CpuMillis, v.MemoryMiB, v.UnboundedInventoryHash, m.PublicKey, a.controller, generation); e != nil {
				return e
			}
		}
		out, e = readNetwork(ctx, tx, ws)
		return e
	})
	return out, err
}

// advanceNetworkGeneration moves the workspace's counter past observed and stamps every member
// with the result. A counter at its limit stays there rather than fail, because revocation runs
// in the same transaction, and every member goes back to pending, since none can be assumed to
// hold what changed.
func advanceNetworkGeneration(ctx context.Context, tx pgx.Tx, ws uuid.UUID, observed int64) error {
	q := db.New(tx)
	next, err := q.AdvanceNetworkGeneration(ctx, db.AdvanceNetworkGenerationParams{ObservedGeneration: observed, WorkspaceID: ws})
	if err != nil {
		return err
	}
	if err = q.SetNetworkMemberGenerations(ctx, db.SetNetworkMemberGenerationsParams{Generation: next, UnaccountedNote: unaccountedNetworkGenerationNote, RouteConflictNote: routeConflictNetworkNote, WorkspaceID: ws}); err != nil || next != math.MaxInt64 {
		return err
	}
	return q.MarkNetworkGenerationExhausted(ctx, db.MarkNetworkGenerationExhaustedParams{Note: exhaustedNetworkGenerationNote, WorkspaceID: ws})
}

// Invalidate network authority in the same transaction as bearer revocation. It has to succeed
// whatever the counter holds: peers learn about the removal through a later generation, but the
// credential must stop working now, so a counter at its limit costs only the peer update.
func invalidateNetworkMember(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID) error {
	disabled, err := db.New(tx).DisableNetworkMember(ctx, db.DisableNetworkMemberParams{WorkspaceID: ws, ServerID: server})
	if err != nil || disabled == 0 {
		return err
	}
	return advanceNetworkGeneration(ctx, tx, ws, 0)
}

type networkSweepArgs struct{}

func (networkSweepArgs) Kind() string { return "platform_network_sweep_v1" }

type networkSweepWorker struct {
	river.WorkerDefaults[networkSweepArgs]
	service *Service
}

func (w *networkSweepWorker) Work(ctx context.Context, _ *river.Job[networkSweepArgs]) error {
	return w.service.SweepNetworks(ctx)
}

// SweepNetworks gives each workspace's network a turn. The worker runs it every five seconds.
func (s *Service) SweepNetworks(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, sweepPassBudget)
	defer cancel()
	started, err := s.Store.Queries.SweepPassStarted(ctx)
	if err != nil {
		return err
	}
	for {
		ws, err := s.Store.Queries.ClaimNetworkForSweep(ctx, started)
		if errors.Is(err, pgx.ErrNoRows) || ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		err = sweep.Item(ctx, "network", ws, sweepItemLimit, func(item context.Context) error { return sweepFailure(s.reconcileNetwork(item, ws)) })
		if ctx.Err() != nil {
			return nil
		}
		if err = s.Store.Queries.RecordNetworkSweepOutcome(ctx, db.RecordNetworkSweepOutcomeParams{Failed: err != nil, WorkspaceID: ws}); err != nil && ctx.Err() == nil {
			return err
		}
	}
}
func (s *Service) reconcileNetwork(ctx context.Context, ws uuid.UUID) error {
	tx, done, err := s.Store.BeginWorkspaceWrite(ctx, ws)
	if err != nil {
		return err
	}
	defer done()
	n, err := readNetwork(ctx, tx, ws)
	if err != nil {
		return err
	}
	for _, m := range n.Members {
		// A member whose claim was not accounted for would answer the same again. Anything new for
		// it advances the generation, which clears the note.
		if m.Acknowledged == m.Generation && m.PublicKey != "" || !m.Enabled && m.PublicKey == "" || m.Note == unaccountedNetworkGenerationNote {
			continue
		}
		var ready bool
		var controller int64
		var reserved bool
		var busy, restriction bool
		err = tx.QueryRow(ctx, `SELECT NOT revoked AND NOT awaiting_transfer AND NOT preview_only AND role<>'build' AND last_seen>now()-interval '45 seconds',controller_generation,EXISTS(SELECT 1 FROM cross_host_reservations WHERE server_id=$1),EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND NOT(kind='crosshost' AND parent_id IS NULL) AND state NOT IN ('succeeded','failed','cancelled')),EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND kind='network' AND command->'network'->>'action'='restrict' AND state NOT IN ('succeeded','failed','cancelled')) FROM servers WHERE id=$1 FOR UPDATE`, m.ServerID).Scan(&ready, &controller, &reserved, &busy, &restriction)
		if err != nil {
			return err
		}
		if !ready || restriction {
			continue
		}
		// While a host is occupied, or until the owner approves its new controller, only removals
		// go out. They need no approval and must reach it.
		held := reserved || busy || controller != m.Controller
		command := hostnetwork.Command{Action: "identify", NetworkID: n.ID, MembershipID: m.MembershipID}
		var removal *hostnetwork.Command
		if m.PublicKey != "" {
			manifest, complete, e := networkManifest(ctx, tx, ws, n, m)
			if e != nil {
				return e
			}
			command.Action = "configure"
			command.Manifest = &manifest
			var installed []byte
			if e = tx.QueryRow(ctx, `SELECT installed_manifest FROM network_members WHERE server_id=$1`, m.ServerID).Scan(&installed); e != nil {
				return e
			}
			var prior hostnetwork.Manifest
			// A re-added member has a new membership, and a record of the old one cannot scope it.
			known := json.Unmarshal(installed, &prior) == nil && prior.Validate() == nil && prior.MembershipID == m.MembershipID
			sameController := known && prior.ControllerGeneration == manifest.ControllerGeneration
			// Hosts refuse changed content at a generation they already hold, and this one
			// cannot rise. The member stays pending, with a note, rather than fail on every pass.
			if sameController && prior.Generation == math.MaxInt64 && manifest.Generation == math.MaxInt64 && hostnetwork.Hash(prior) != hostnetwork.Hash(manifest) {
				continue
			}
			// After a restore the record of what this host holds is out of date, so it cannot scope
			// a removal. The owner approved this controller's whole manifest, which replaces
			// whatever the host holds, including peers the restored backup never knew about, and
			// must not wait for a new peer that may never report a key.
			if held || m.Note == refusedNetworkConfigureNote || m.Note == routeConflictNetworkNote || !complete && sameController {
				if !known {
					continue
				}
				landing, e := db.New(tx).NetworkManifestLanding(ctx, db.NetworkManifestLandingParams{WorkspaceID: ws, ServerID: m.ServerID, Generation: m.Generation})
				if e != nil {
					return e
				}
				if landing {
					continue
				}
				manifest = hostnetwork.Restriction(prior, manifest)
				if manifest.Generation == prior.Generation {
					continue
				}
				command.Manifest = &manifest
				if !hostnetwork.Restricts(prior, manifest) {
					continue
				}
				command.Action = "restrict"
			}
			if known && (command.Action == "restrict" || sameController && hostnetwork.Restricts(prior, manifest)) {
				command.Action = "restrict"
				command.Baseline = &prior
			} else if known {
				reduced := hostnetwork.Restriction(prior, manifest)
				if reduced.Generation != prior.Generation && hostnetwork.Restricts(prior, reduced) {
					removal = &hostnetwork.Command{Action: "restrict", NetworkID: n.ID, MembershipID: m.MembershipID, Manifest: &reduced, Baseline: &prior}
				}
			}
		}
		if held && command.Action != "restrict" {
			continue
		}
		var generation int64
		if err = tx.QueryRow(ctx, `UPDATE servers SET update_generation=update_generation+1 WHERE id=$1 RETURNING update_generation`, m.ServerID).Scan(&generation); err != nil {
			return err
		}
		commands := []hostnetwork.Command{command}
		if removal != nil {
			commands = append(commands, *removal)
		}
		for _, candidate := range commands {
			raw, _ := stablejson.Marshal(candidate)
			c := api.ExecutionCommand{Id: uuid.New(), ServerId: m.ServerID, Generation: generation, Kind: "network", Action: "inspect", ResourceId: n.ID, PlanHash: hostnetwork.Hash(candidate), Deadline: time.Now().Add(5 * time.Minute), Network: rawMessage(raw)}
			if _, err = s.insert(ctx, tx, ws, uuid.New(), nil, c); err != nil {
				// Admission failures precede operation writes. A denied addition
				// must still yield to recovery-authorized removals and other members.
				var problem *domain.Error
				if errors.As(err, &problem) {
					continue
				}
				return err
			}
			break
		}
	}
	return tx.Commit(ctx)
}
func networkManifest(ctx context.Context, tx pgx.Tx, ws uuid.UUID, n managedNetwork, m networkMember) (hostnetwork.Manifest, bool, error) {
	p := hostnetwork.Manifest{Version: hostnetwork.Version, NetworkID: n.ID, MembershipID: m.MembershipID, ServerID: m.ServerID, ControllerGeneration: m.Controller, Generation: m.Generation, Prefix: n.Prefix, Address: m.Address, PublicKey: m.PublicKey, ListenPort: m.ListenPort, Peers: []hostnetwork.Peer{}, Exports: []hostnetwork.Endpoint{}, Egress: []hostnetwork.Egress{}}
	if !m.Enabled {
		p.Disabled = true
		return p, true, p.Validate()
	}
	ready := true
	for _, peer := range n.Members {
		if !peer.Enabled || peer.ServerID == m.ServerID {
			continue
		}
		if peer.PublicKey == "" {
			ready = false
			continue
		}
		p.Peers = append(p.Peers, hostnetwork.Peer{ServerID: peer.ServerID, MembershipID: peer.MembershipID, Address: peer.Address, PublicKey: peer.PublicKey, Endpoint: peer.Endpoint})
	}
	rows, err := tx.Query(ctx, `SELECT id,unit_id,resource_id,instance_id,listen_port,container_port FROM network_endpoints WHERE workspace_id=$1 AND server_id=$2 AND NOT retired ORDER BY id`, ws, m.ServerID)
	if err != nil {
		return p, false, err
	}
	for rows.Next() {
		var e hostnetwork.Endpoint
		if err = rows.Scan(&e.ID, &e.UnitID, &e.ResourceID, &e.InstanceID, &e.Port, &e.ContainerPort); err != nil {
			rows.Close()
			return p, false, err
		}
		p.Exports = append(p.Exports, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, false, err
	}
	sources, err := tx.Query(ctx, `SELECT s.endpoint_id,s.server_id,s.unit_id FROM network_endpoint_sources s JOIN network_endpoints e ON e.workspace_id=s.workspace_id AND e.id=s.endpoint_id JOIN network_members m ON m.workspace_id=s.workspace_id AND m.server_id=s.server_id WHERE s.workspace_id=$1 AND e.server_id=$2 AND NOT e.retired AND m.enabled AND m.public_key<>'' AND s.active ORDER BY s.endpoint_id,s.server_id,s.unit_id`, ws, m.ServerID)
	if err != nil {
		return p, false, err
	}
	bySource := map[uuid.UUID][]hostnetwork.Source{}
	for sources.Next() {
		var endpoint uuid.UUID
		var source hostnetwork.Source
		if err = sources.Scan(&endpoint, &source.ServerID, &source.UnitID); err != nil {
			sources.Close()
			return p, false, err
		}
		bySource[endpoint] = append(bySource[endpoint], source)
	}
	err = sources.Err()
	sources.Close()
	if err != nil {
		return p, false, err
	}
	for i := range p.Exports {
		p.Exports[i].Sources = bySource[p.Exports[i].ID]
	}
	filtered := p.Exports[:0]
	for _, endpoint := range p.Exports {
		if len(endpoint.Sources) > 0 {
			filtered = append(filtered, endpoint)
		}
	}
	p.Exports = filtered
	rows, err = tx.Query(ctx, `SELECT s.unit_id,e.server_id,e.id,e.listen_port,s.ingress FROM network_endpoint_sources s JOIN network_endpoints e ON e.workspace_id=s.workspace_id AND e.id=s.endpoint_id JOIN network_members m ON m.workspace_id=e.workspace_id AND m.server_id=e.server_id WHERE s.workspace_id=$1 AND s.server_id=$2 AND m.enabled AND m.public_key<>'' AND s.active AND NOT e.retired ORDER BY s.unit_id,e.id`, ws, m.ServerID)
	if err != nil {
		return p, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var e hostnetwork.Egress
		if err = rows.Scan(&e.UnitID, &e.ServerID, &e.EndpointID, &e.Port, &e.Ingress); err != nil {
			return p, false, err
		}
		p.Egress = append(p.Egress, e)
	}
	if err = rows.Err(); err != nil {
		return p, false, err
	}
	p.Sort()
	return p, ready, p.Validate()
}

// Revoking a server takes away its management credential and its peers' trust, but nothing can
// reach the host itself, so these say what is and is not known about it.
const (
	revokedMemberPendingNote = "This revoked host may still hold its own private network configuration, and it cannot confirm removing it. Not every remaining member has confirmed the current configuration, so some may still accept its key."
	revokedMemberRemovedNote = "This revoked host may still hold its own private network configuration, and it cannot confirm removing it. Every remaining member has confirmed a configuration without its key, so none of them accepts it."
)

const renewedNetworkApprovalNote = "Controller recovery preserves private links; general network mutation requires renewed approval."

const exhaustedNetworkGenerationNote = "This network's generation has reached its limit, so a server that already holds it cannot receive further changes."
