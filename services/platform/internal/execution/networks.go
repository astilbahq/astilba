package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/store"
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
	rows, err := tx.Query(ctx, `SELECT server_id,membership_id,host(address),endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,enabled,generation,acknowledged_generation,note,approved_controller,manifest_hash FROM network_members WHERE workspace_id=$1 ORDER BY server_id`, ws)
	if err != nil {
		return n, err
	}
	defer rows.Close()
	n.State = "ready"
	for rows.Next() {
		var m networkMember
		if err = rows.Scan(&m.ServerID, &m.MembershipID, &m.Address, &m.Endpoint, &m.ListenPort, &m.CPUMillis, &m.MemoryMiB, &m.InventoryHash, &m.PublicKey, &m.Enabled, &m.Generation, &m.Acknowledged, &m.Note, &m.Controller, &m.ManifestHash); err != nil {
			return n, err
		}
		m.State = "removed"
		if !m.Enabled && m.PublicKey != "" && m.Acknowledged != m.Generation {
			m.State = "revocation_pending"
			n.State = "pending"
		}
		if m.Enabled {
			m.State = "pending"
			if m.Acknowledged == m.Generation && m.PublicKey != "" {
				m.State = "ready"
			} else {
				n.State = "pending"
			}
		}
		n.Members = append(n.Members, m)
	}
	return n, rows.Err()
}
func (s *Service) ManagedNetwork(ctx context.Context, p store.Principal, ws uuid.UUID) (managedNetwork, error) {
	var n managedNetwork
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error { var e error; n, e = readNetwork(ctx, tx, ws); return e })
	return n, err
}
func (s *Service) ConfigureManagedNetwork(ctx context.Context, p store.Principal, ws uuid.UUID, in api.ConfigureManagedNetwork) (managedNetwork, error) {
	var out managedNetwork
	prefix, err := hostnetwork.Prefix(in.Prefix)
	if err != nil || len(in.Members) > hostnetwork.MaxMembers {
		return out, domain.Invalid("network")
	}
	err = s.scope(ctx, p, ws, true, true, true, "network.configure", ws, func(tx pgx.Tx) error {
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
		if _, e = tx.Exec(ctx, `UPDATE network_members SET enabled=false,generation=$2,note='' WHERE workspace_id=$1`, ws, generation); e != nil {
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

// Invalidate network authority in the same transaction as bearer revocation.
func invalidateNetworkMember(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE network_members SET enabled=false,note='Server authority revoked; peer removals await acknowledgement.' WHERE workspace_id=$1 AND server_id=$2 AND enabled RETURNING network_id`, ws, server).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var generation int64
	if err = tx.QueryRow(ctx, `UPDATE workspace_networks SET generation=generation+1 WHERE id=$1 RETURNING generation`, id).Scan(&generation); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE network_members SET generation=$2 WHERE network_id=$1`, id, generation)
	return err
}

type networkSweepArgs struct{}

func (networkSweepArgs) Kind() string { return "platform_network_sweep_v1" }

type networkSweepWorker struct {
	river.WorkerDefaults[networkSweepArgs]
	service *Service
}

func (w *networkSweepWorker) Work(ctx context.Context, _ *river.Job[networkSweepArgs]) error {
	return w.service.reconcileNetworks(ctx)
}
func (s *Service) reconcileNetworks(ctx context.Context) error {
	rows, err := s.Store.Pool.Query(ctx, `SELECT workspace_id FROM workspace_networks ORDER BY checked_at,workspace_id LIMIT 1000`)
	if err != nil {
		return err
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ws := range ids {
		err = s.reconcileNetwork(ctx, ws)
		var problem *domain.Error
		if err != nil && !errors.As(err, &problem) && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = s.Store.Pool.Exec(ctx, `UPDATE workspace_networks SET checked_at=clock_timestamp() WHERE workspace_id=$1`, ws); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) reconcileNetwork(ctx context.Context, ws uuid.UUID) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM workspaces WHERE id=$1 FOR UPDATE`, ws); err != nil {
		return err
	}
	n, err := readNetwork(ctx, tx, ws)
	if err != nil {
		return err
	}
	for _, m := range n.Members {
		if m.Acknowledged == m.Generation && m.PublicKey != "" || !m.Enabled && m.PublicKey == "" {
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
		if !ready || controller != m.Controller || restriction {
			continue
		}
		command := hostnetwork.Command{Action: "identify", NetworkID: n.ID, MembershipID: m.MembershipID}
		var removal *hostnetwork.Command
		if m.PublicKey != "" {
			manifest, ready, e := networkManifest(ctx, tx, ws, n, m)
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
			known := json.Unmarshal(installed, &prior) == nil && prior.Validate() == nil
			if reserved || busy || !ready {
				if !known {
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
			if known && hostnetwork.Restricts(prior, manifest) {
				command.Action = "restrict"
			}
			if command.Action == "restrict" {
				command.Baseline = &prior
			} else if known {
				reduced := hostnetwork.Restriction(prior, manifest)
				if reduced.Generation != prior.Generation && hostnetwork.Restricts(prior, reduced) {
					removal = &hostnetwork.Command{Action: "restrict", NetworkID: n.ID, MembershipID: m.MembershipID, Manifest: &reduced, Baseline: &prior}
				}
			}
		}
		if (reserved || busy) && command.Action != "restrict" {
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
			raw, _ := json.Marshal(candidate)
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
	for i := range p.Exports {
		e := &p.Exports[i]
		sources, e2 := tx.Query(ctx, `SELECT s.server_id,s.unit_id FROM network_endpoint_sources s JOIN network_members m ON m.workspace_id=s.workspace_id AND m.server_id=s.server_id WHERE s.workspace_id=$1 AND s.endpoint_id=$2 AND m.enabled AND m.public_key<>'' AND s.active ORDER BY s.server_id,s.unit_id`, ws, e.ID)
		if e2 != nil {
			return p, false, e2
		}
		for sources.Next() {
			var source hostnetwork.Source
			if e2 = sources.Scan(&source.ServerID, &source.UnitID); e2 != nil {
				sources.Close()
				return p, false, e2
			}
			e.Sources = append(e.Sources, source)
		}
		e2 = sources.Err()
		sources.Close()
		if e2 != nil {
			return p, false, e2
		}
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
func (s *Service) networkReport(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, r *api.AgentReport) error {
	if c.Kind != "network" {
		if r.Network != nil {
			return domain.Invalid("network")
		}
		return nil
	}
	if c.Network == nil {
		return domain.Invalid("network")
	}
	var cmd hostnetwork.Command
	if hostnetwork.Decode(*c.Network, &cmd) != nil || cmd.Validate() != nil || hostnetwork.Hash(cmd) != c.PlanHash {
		return domain.Invalid("network")
	}
	if r.State != "succeeded" {
		return nil
	}
	if r.Network == nil {
		return domain.Invalid("network")
	}
	var receipt hostnetwork.Receipt
	if hostnetwork.Decode(*r.Network, &receipt) != nil || receipt.NetworkID != cmd.NetworkID || receipt.Identity.MembershipID != cmd.MembershipID || !hostnetwork.PublicKey(receipt.Identity.PublicKey) {
		return domain.Invalid("network")
	}
	if cmd.Action == "identify" {
		_, err := tx.Exec(ctx, `UPDATE network_members SET public_key=$3,note='' WHERE server_id=$1 AND membership_id=$2 AND enabled AND public_key=''`, c.ServerId, cmd.MembershipID, receipt.Identity.PublicKey)
		return err
	}
	if cmd.Manifest.ServerID != c.ServerId || receipt.Identity.PublicKey != cmd.Manifest.PublicKey {
		return domain.Invalid("network.receipt")
	}
	observed, err := reconciledNetworkReceipt(ctx, tx, c, cmd, receipt)
	if err != nil {
		return err
	}
	var ws uuid.UUID
	var installed []byte
	if err = tx.QueryRow(ctx, `SELECT workspace_id,installed_manifest FROM network_members WHERE server_id=$1 AND membership_id=$2 AND public_key=$3 FOR UPDATE`, c.ServerId, cmd.MembershipID, receipt.Identity.PublicKey).Scan(&ws, &installed); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var known hostnetwork.Manifest
	if json.Unmarshal(installed, &known) == nil && known.Validate() == nil {
		if observed.ControllerGeneration < known.ControllerGeneration || observed.Generation < known.Generation {
			return nil
		}
		if observed.Generation == known.Generation && hostnetwork.Hash(observed) != hostnetwork.Hash(known) {
			return domain.Invalid("network.generation")
		}
	}
	n, err := readNetwork(ctx, tx, ws)
	if err != nil {
		return err
	}
	for _, member := range n.Members {
		if member.ServerID != c.ServerId {
			continue
		}
		desired, ready, err := networkManifest(ctx, tx, ws, n, member)
		if err != nil {
			return err
		}
		converged := ready && sameNetworkConfiguration(desired, observed) && member.Controller == observed.ControllerGeneration && member.Controller == generation(c.ControllerGeneration)
		ack := int64(0)
		note := ""
		if converged {
			ack = member.Generation
		} else if member.Controller != observed.ControllerGeneration || member.Controller != generation(c.ControllerGeneration) {
			note = "Controller recovery preserves private links; general network mutation requires renewed approval."
		}
		if _, err = tx.Exec(ctx, `UPDATE network_members SET acknowledged_generation=$3,manifest_hash=$4,note=$5,installed_manifest=$6 WHERE server_id=$1 AND membership_id=$2`, c.ServerId, cmd.MembershipID, ack, receipt.Hash, note, mustJSON(observed)); err != nil {
			return err
		}
		if converged || observed.Generation < member.Generation {
			return nil
		}
		// Partial removals can preserve additions whose configure receipt was
		// lost. The next full intent must not collide with observed authority.
		var next int64
		if err = tx.QueryRow(ctx, `UPDATE workspace_networks SET generation=GREATEST(generation,$2)+1 WHERE workspace_id=$1 RETURNING generation`, ws, observed.Generation).Scan(&next); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE network_members SET generation=$2 WHERE workspace_id=$1`, ws, next)
		return err
	}
	return nil
}

func sameNetworkConfiguration(a, b hostnetwork.Manifest) bool {
	// Empty grant lists mean the same thing after either JSON null or [] decoding.
	// Normalize copies only; frozen command and receipt digests retain their encoding.
	for _, m := range []*hostnetwork.Manifest{&a, &b} {
		if len(m.Peers) == 0 {
			m.Peers = nil
		}
		if len(m.Exports) == 0 {
			m.Exports = nil
		}
		if len(m.Egress) == 0 {
			m.Egress = nil
		}
	}
	return hostnetwork.Hash(a) == hostnetwork.Hash(b)
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
