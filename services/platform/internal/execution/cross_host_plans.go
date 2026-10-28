package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type coordinationCommand struct {
	Action      string                  `json:"action"`
	Plan        deployment.Plan         `json:"plan"`
	Maintenance *coordinatedMaintenance `json:"maintenance,omitempty"`
}

func previousUnits(p deployment.Plan) map[uuid.UUID]deployment.Plan {
	out := map[uuid.UUID]deployment.Plan{}
	if p.ID == uuid.Nil {
		return out
	}
	if p.Distributed != nil {
		for _, u := range p.Distributed.Units {
			if len(u.Plan.LocalApplications()) > 0 {
				out[u.Plan.ServerID] = u.Plan
			}
		}
	} else {
		out[p.ServerID] = p
	}
	return out
}
func (s *Service) createCrossHostPlan(ctx context.Context, tx pgx.Tx, ws, pid, env uuid.UUID, in api.VisualPlanRequest, v visualEnvironment, previous deployment.Plan) (deployment.Status, error) {
	out := deployment.Status{}
	if v.Purpose == "preview" || v.Spec.Compose != nil || in.AcceptDrift || v.Spec.Placement == nil {
		return out, domain.Invalid("placement")
	}
	if v.Spec.Placement.IngressServerID != in.ServerId || v.Server != nil && *v.Server != in.ServerId {
		return out, domain.Conflict
	}
	if err := s.Store.ValidateApplicationRoutes(ctx, tx, ws, env, v.Spec, &in.ServerId); err != nil {
		return out, err
	}
	if !in.AllowHostChanges {
		return out, domain.RecentRequired
	}
	if err := s.validateVisualSecrets(ctx, tx, ws, env, v.Spec); err != nil {
		return out, err
	}
	network, err := readNetwork(ctx, tx, ws)
	if err != nil {
		return out, err
	}
	if network.ID != v.Spec.Placement.NetworkID || network.State != "ready" {
		return out, domain.Invalid("placement.network")
	}
	prior := previousUnits(previous)
	members := map[uuid.UUID]networkMember{}
	for _, m := range network.Members {
		if m.Enabled {
			members[m.ServerID] = m
		}
	}
	selected := map[uuid.UUID]bool{in.ServerId: true}
	for _, group := range v.Spec.Placement.Applications {
		for _, target := range group.Targets {
			selected[target.ServerID] = true
		}
	}
	for server := range prior {
		selected[server] = true
	}
	if len(selected) > 4 {
		return out, domain.Invalid("placement.servers")
	}
	distributed := map[uuid.UUID]domain.ApplicationPlacement{}
	for _, group := range v.Spec.Placement.Applications {
		distributed[group.ApplicationID] = group
	}
	boundary, err := readRestoreCutover(ctx, tx, ws, env, v.Revision)
	if err == nil {
		return s.createRestoreReview(ctx, tx, ws, in, v, previous, network, boundary)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if previous.ID != uuid.Nil {
		if deployment.Hash(previous.Specification.Variables) != deployment.Hash(v.Spec.Variables) || deployment.Hash(previous.Specification.Volumes) != deployment.Hash(v.Spec.Volumes) {
			return out, domain.Invalid("placement.sharedChange")
		}
		for _, a := range v.Spec.Applications {
			if _, ok := distributed[a.ID]; ok {
				continue
			}
			found := false
			for _, old := range previous.Specification.Applications {
				if old.ID == a.ID {
					found = deployment.Hash(old) == deployment.Hash(a)
				}
			}
			if !found {
				return out, domain.Invalid("placement.sharedChange")
			}
		}
	}
	if previous.ID == uuid.Nil {
		return out, domain.Invalid("placement.initializePrimaryFirst")
	}
	oldPorts, newPorts := requiredPorts(previous.Specification), requiredPorts(v.Spec)
	sort.Ints(oldPorts)
	sort.Ints(newPorts)
	if deployment.Hash(oldPorts) != deployment.Hash(newPorts) {
		return out, domain.Invalid("placement.ingressPorts")
	}

	parent := deployment.Plan{ID: uuid.New(), ServerID: in.ServerId, EnvironmentID: env, UnitID: v.Unit, RevisionID: v.Revision, ExpectedAppliedID: v.Applied, Recipe: deployment.CrossHostRecipe, Specification: v.Spec, Images: map[string]string{}, HostChanges: true, Effects: []string{"Prepare explicit cross-host pools; publish only after every host and private endpoint is ready; retain old pools while outcomes are uncertain.", "First publication of an existing private service port can restart that service on its owning host."}, ExpiresAt: time.Now().UTC().Add(15 * time.Minute), Distributed: &deployment.DistributedPlan{NetworkID: network.ID, NetworkGeneration: network.Generation + 1, Units: []deployment.DistributedUnit{}}}
	if in.ReleaseOptions != nil {
		parent.ReleaseOptions = &deployment.ReleaseOptions{Command: in.ReleaseOptions.Command, CommandTimeoutSeconds: in.ReleaseOptions.CommandTimeoutSeconds, DrainSeconds: in.ReleaseOptions.DrainSeconds}
		if err = parent.ReleaseOptions.Validate(); err != nil {
			return out, domain.Invalid("releaseOptions")
		}
		if len(parent.ReleaseOptions.Command) > 0 && len(distributed) != 1 {
			return out, domain.Invalid("releaseOptions.application")
		}
	}
	servers := []uuid.UUID{}
	for server := range selected {
		servers = append(servers, server)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].String() < servers[j].String() })
	for _, server := range servers {
		member, ok := members[server]
		if !ok {
			return out, domain.Invalid("placement.member")
		}
		if _, err = s.visualServer(ctx, tx, ws, server); err != nil {
			return out, err
		}
		var cap, pre []byte
		var controller int64
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT capabilities,preflight,controller_generation,EXISTS(SELECT 1 FROM cross_host_reservations WHERE server_id=$2) FROM servers WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, server).Scan(&cap, &pre, &controller, &busy); err != nil {
			return out, err
		}
		var caps api.AgentCapabilities
		var host api.HostPreflight
		if busy || controller != member.Controller || json.Unmarshal(cap, &caps) != nil || caps.CrossHost == nil || !*caps.CrossHost || json.Unmarshal(pre, &host) != nil || host.CrossHostInventory == nil || host.CrossHostInventory.UnboundedInventoryHash != member.InventoryHash {
			return out, domain.Invalid("placement.capacity")
		}
		if server == in.ServerId && (host.CrossHostInventory.IngressReady == nil || !*host.CrossHostInventory.IngressReady) {
			return out, domain.Invalid("placement.preparePrimaryIngress")
		}
		unitID := v.Unit
		if server != in.ServerId {
			err = tx.QueryRow(ctx, `SELECT id FROM deployment_units WHERE workspace_id=$1 AND environment_id=$2 AND server_id=$3 AND NOT primary_unit`, ws, env, server).Scan(&unitID)
			if errors.Is(err, pgx.ErrNoRows) {
				unitID = uuid.New()
				_, err = tx.Exec(ctx, `INSERT INTO deployment_units(workspace_id,id,environment_id,server_id,primary_unit) VALUES($1,$2,$3,$4,false)`, ws, unitID, env, server)
			}
			if err != nil {
				return out, err
			}
		}
		local := deployment.LocalPlacement{GroupID: parent.ID, Primary: server == in.ServerId, Instances: map[string][]uuid.UUID{}, PriorInstances: map[string][]uuid.UUID{}, Requests: map[string]deployment.ResourceRequest{}, Outputs: map[string]deployment.PrivateEndpoint{}, Backends: map[string][]deployment.PrivateEndpoint{}}
		old := prior[server]
		if old.ID != uuid.Nil {
			for _, app := range old.Specification.Applications {
				if ids := old.InstanceIDs(app.ID); len(ids) > 0 {
					local.PriorInstances[app.ID.String()] = ids
				}
			}
		}
		for _, app := range v.Spec.Applications {
			group, isDistributed := distributed[app.ID]
			count := 0
			if isDistributed {
				for _, target := range group.Targets {
					if target.ServerID == server {
						count = target.Count
					}
				}
			} else if local.Primary {
				count = app.ReplicaCount()
			}
			if count == 0 {
				continue
			}
			ids := []uuid.UUID{}
			for i := 0; i < count; i++ {
				id := uuid.New()
				if !isDistributed {
					if old.ID != uuid.Nil && len(old.InstanceIDs(app.ID)) > i {
						id = old.InstanceIDs(app.ID)[i]
					} else if app.Postgres != nil {
						id = app.ID
					}
				}
				ids = append(ids, id)
			}
			local.Instances[app.ID.String()] = ids
			if isDistributed {
				local.Requests[app.ID.String()] = deployment.ResourceRequest{CPUMillis: group.CPUMillis, MemoryMiB: group.MemoryMiB}
			}
		}
		manifest, ready, e := networkManifest(ctx, tx, ws, network, member)
		if e != nil {
			return out, e
		}
		if !ready {
			return out, domain.Invalid("placement.network")
		}
		manifest.Generation = parent.Distributed.NetworkGeneration
		local.Network = manifest
		plan := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: env, UnitID: unitID, RevisionID: v.Revision, ExpectedAppliedID: v.Applied, Recipe: deployment.CrossHostRecipe, Specification: v.Spec, Images: map[string]string{}, Local: &local, HostChanges: true, ExpiresAt: parent.ExpiresAt, ReleaseOptions: parent.ReleaseOptions}
		if err = s.planArtifacts(ctx, tx, ws, &plan); err != nil {
			return out, err
		}
		var cpu, mem int64
		for key, request := range local.Requests {
			cpu += int64(request.CPUMillis * len(local.Instances[key]))
			mem += int64(request.MemoryMiB * len(local.Instances[key]))
		}
		parent.Distributed.Units = append(parent.Distributed.Units, deployment.DistributedUnit{Plan: plan, Previous: old, CPUMillis: cpu, MemoryMiB: mem, InventoryHash: member.InventoryHash})
	}
	if err = s.preparePrivateEndpoints(ctx, tx, ws, &parent); err != nil {
		return out, err
	}
	if parent.ReleaseOptions != nil && len(parent.ReleaseOptions.Command) > 0 {
		for i := range parent.Distributed.Units {
			u := &parent.Distributed.Units[i]
			app := v.Spec.Placement.Applications[0].ApplicationID
			if u.Plan.Runs(app) {
				u.Plan.Release = &deployment.Release{ApplicationID: app, PreviousPlanID: previous.ID, Options: *parent.ReleaseOptions}
				break
			}
		}
	}
	for i := range parent.Distributed.Units {
		parent.Distributed.Units[i].Plan = parent.Distributed.Units[i].Plan.WithHash()
	}
	parent = parent.WithHash()
	if err = parent.Validate(false); err != nil {
		return out, domain.Invalid("placement.plan")
	}
	if err = checkCrossHostCapacity(ctx, tx, ws, parent); err != nil {
		return out, err
	}
	return s.insertCrossHostReview(ctx, tx, ws, parent)
}

func (s *Service) insertCrossHostReview(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent deployment.Plan) (deployment.Status, error) {
	op, err := s.insertCoordination(ctx, tx, ws, uuid.New(), "resolve", parent)
	if err != nil {
		return deployment.Status{}, err
	}
	raw, _ := json.Marshal(parent)
	if len(raw) > 1000000 {
		return deployment.Status{}, domain.Invalid("placement.size")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, parent.ID, ws, parent.ServerID, parent.EnvironmentID, parent.UnitID, parent.RevisionID, op.Id, raw); err != nil {
		return deployment.Status{}, err
	}
	return deployment.Status{ID: parent.ID, OperationID: op.Id, State: string(op.State)}, nil
}

func copyNetworkPlanAssociations(ctx context.Context, tx pgx.Tx, ws, source, target uuid.UUID) error {
	if _, err := tx.Exec(ctx, `INSERT INTO network_plan_endpoints(plan_id,workspace_id,endpoint_id) SELECT $1,workspace_id,endpoint_id FROM network_plan_endpoints WHERE workspace_id=$2 AND plan_id=$3`, target, ws, source); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO network_plan_sources(plan_id,workspace_id,endpoint_id,server_id,unit_id,ingress) SELECT $1,workspace_id,endpoint_id,server_id,unit_id,ingress FROM network_plan_sources WHERE workspace_id=$2 AND plan_id=$3`, target, ws, source)
	return err
}
func endpointKey(server, instance uuid.UUID, port int) string {
	return fmt.Sprintf("%s/%s/%d", server, instance, port)
}
func (s *Service) preparePrivateEndpoints(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent *deployment.Plan) error {
	units := parent.Distributed.Units
	endpointMap := map[string]hostnetwork.Endpoint{}
	used := map[uuid.UUID]map[int]bool{}
	rows, err := tx.Query(ctx, `SELECT id,server_id,unit_id,resource_id,instance_id,listen_port,container_port FROM network_endpoints WHERE workspace_id=$1 ORDER BY server_id,listen_port`, ws)
	if err != nil {
		return err
	}
	for rows.Next() {
		var e hostnetwork.Endpoint
		var server uuid.UUID
		if err = rows.Scan(&e.ID, &server, &e.UnitID, &e.ResourceID, &e.InstanceID, &e.Port, &e.ContainerPort); err != nil {
			rows.Close()
			return err
		}
		endpointMap[endpointKey(server, e.InstanceID, e.ContainerPort)] = e
		if used[server] == nil {
			used[server] = map[int]bool{}
		}
		used[server][e.Port] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	ensure := func(dest int, resource, instance uuid.UUID, port int, source int, ingress bool) (deployment.PrivateEndpoint, error) {
		u, from := &units[dest], &units[source]
		key := endpointKey(u.Plan.ServerID, instance, port)
		e, exists := endpointMap[key]
		if !exists {
			if used[u.Plan.ServerID] == nil {
				used[u.Plan.ServerID] = map[int]bool{}
			}
			listen := hostnetwork.FirstEndpointPort
			for used[u.Plan.ServerID][listen] && listen <= hostnetwork.LastEndpointPort {
				listen++
			}
			if listen > hostnetwork.LastEndpointPort {
				return deployment.PrivateEndpoint{}, domain.Invalid("placement.ports")
			}
			used[u.Plan.ServerID][listen] = true
			e = hostnetwork.Endpoint{ID: uuid.New(), UnitID: u.Plan.UnitID, ResourceID: resource, InstanceID: instance, Port: listen, ContainerPort: port, Sources: []hostnetwork.Source{}}
			if _, err = tx.Exec(ctx, `INSERT INTO network_endpoints(id,workspace_id,environment_id,server_id,unit_id,resource_id,instance_id,listen_port,container_port,retired,plan_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,true,$10)`, e.ID, ws, parent.EnvironmentID, u.Plan.ServerID, e.UnitID, resource, instance, listen, port, parent.ID); err != nil {
				return deployment.PrivateEndpoint{}, err
			}
		}
		sourceGrant := hostnetwork.Source{ServerID: from.Plan.ServerID, UnitID: from.Plan.UnitID}
		if !slices.Contains(e.Sources, sourceGrant) {
			e.Sources = append(e.Sources, sourceGrant)
		}
		endpointMap[key] = e
		if _, err = tx.Exec(ctx, `INSERT INTO network_endpoint_sources(workspace_id,endpoint_id,server_id,unit_id,active,plan_id) VALUES($1,$2,$3,$4,false,$5) ON CONFLICT(endpoint_id,server_id,unit_id) DO NOTHING`, ws, e.ID, sourceGrant.ServerID, sourceGrant.UnitID, parent.ID); err != nil {
			return deployment.PrivateEndpoint{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO network_plan_endpoints(plan_id,workspace_id,endpoint_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, parent.ID, ws, e.ID); err != nil {
			return deployment.PrivateEndpoint{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO network_plan_sources(plan_id,workspace_id,endpoint_id,server_id,unit_id,ingress) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, parent.ID, ws, e.ID, sourceGrant.ServerID, sourceGrant.UnitID, ingress); err != nil {
			return deployment.PrivateEndpoint{}, err
		}
		addEgress := hostnetwork.Egress{Ingress: ingress, UnitID: from.Plan.UnitID, ServerID: u.Plan.ServerID, EndpointID: e.ID, Port: e.Port}
		if !slices.Contains(from.Plan.Local.Network.Egress, addEgress) {
			from.Plan.Local.Network.Egress = append(from.Plan.Local.Network.Egress, addEgress)
		}
		return deployment.PrivateEndpoint{ServerID: u.Plan.ServerID, UnitID: u.Plan.UnitID, ResourceID: resource, InstanceID: instance, Address: u.Plan.Local.Network.Address, Port: e.Port, ContainerPort: port}, nil
	}
	primary := -1
	for i := range units {
		if units[i].Plan.Local.Primary {
			primary = i
		}
	}
	if primary < 0 {
		return domain.Invalid("placement.primary")
	}
	for _, route := range parent.Specification.Routes {
		backends := []deployment.PrivateEndpoint{}
		for i := range units {
			for _, instance := range units[i].Plan.InstanceIDs(route.TargetID) {
				e := deployment.PrivateEndpoint{ServerID: units[i].Plan.ServerID, UnitID: units[i].Plan.UnitID, ResourceID: route.TargetID, InstanceID: instance, Address: deployment.ServiceName(instance), Port: route.Port, ContainerPort: route.Port}
				if i != primary {
					e, err = ensure(i, route.TargetID, instance, route.Port, primary, true)
					if err != nil {
						return err
					}
				}
				backends = append(backends, e)
			}
		}
		if len(backends) > 0 {
			prior := units[primary].Plan.Local.Backends[route.TargetID.String()]
			if len(prior) > 0 && prior[0].ContainerPort != route.Port {
				return domain.Invalid("placement.routePorts")
			}
			units[primary].Plan.Local.Backends[route.TargetID.String()] = backends
		}
	}
	for targetIndex := range units {
		for _, binding := range parent.Specification.Bindings {
			if !units[targetIndex].Plan.Runs(binding.TargetID) || units[targetIndex].Plan.Runs(binding.SourceID) {
				continue
			}
			sourceIndex := -1
			var source domain.Application
			for _, a := range parent.Specification.Applications {
				if a.ID == binding.SourceID {
					source = a
				}
			}
			output, ok := domain.BindingOutput(source, binding.Output)
			if !ok {
				return domain.Invalid("placement.bindings")
			}
			if output.Type == "variable" {
				continue
			}
			if output.Type == "address" {
				paired := false
				for _, other := range parent.Specification.Bindings {
					op, ok := domain.BindingOutput(source, other.Output)
					paired = paired || other.SourceID == source.ID && other.TargetID == binding.TargetID && ok && (op.Type == "port" || op.Type == "url")
				}
				if !paired {
					return domain.Invalid("placement.bindings.portRequired")
				}
				continue
			}
			for i := range units {
				if units[i].Plan.Runs(source.ID) {
					sourceIndex = i
				}
			}
			if sourceIndex < 0 || len(units[sourceIndex].Plan.InstanceIDs(source.ID)) != 1 {
				return domain.Invalid("placement.bindings")
			}
			e, x := ensure(sourceIndex, source.ID, units[sourceIndex].Plan.InstanceIDs(source.ID)[0], output.Port, targetIndex, false)
			if x != nil {
				return x
			}
			units[targetIndex].Plan.Local.Outputs[fmt.Sprintf("%s:%d", source.ID, output.Port)] = e
		}
	}
	for i := range units {
		exports := units[i].Plan.Local.Network.Exports
		byID := map[uuid.UUID]int{}
		for j, e := range exports {
			byID[e.ID] = j
		}
		for key, e := range endpointMap {
			_ = key
			if e.UnitID != units[i].Plan.UnitID || len(e.Sources) == 0 {
				continue
			}
			if index, ok := byID[e.ID]; ok {
				for _, source := range e.Sources {
					if !slices.Contains(exports[index].Sources, source) {
						exports[index].Sources = append(exports[index].Sources, source)
					}
				}
			} else {
				exports = append(exports, e)
			}
		}
		sort.Slice(exports, func(a, b int) bool { return exports[a].ID.String() < exports[b].ID.String() })
		units[i].Plan.Local.Network.Exports = exports
		units[i].Plan.Local.Network.Sort()
	}
	parent.Distributed.Units = units
	return nil
}
func checkCrossHostCapacity(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p deployment.Plan) error {
	for _, u := range p.Distributed.Units {
		var cpu, mem, allocatedCPU, allocatedMem int64
		var hash string
		if err := tx.QueryRow(ctx, `SELECT cpu_millis,memory_mib,unbounded_inventory_hash FROM network_members WHERE workspace_id=$1 AND server_id=$2 AND enabled FOR UPDATE`, ws, u.Plan.ServerID).Scan(&cpu, &mem, &hash); err != nil {
			return err
		}
		if hash != u.InventoryHash {
			return domain.Invalid("placement.capacity")
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(cpu_millis),0),COALESCE(sum(memory_mib),0) FROM cross_host_runtime WHERE workspace_id=$1 AND server_id=$2`, ws, u.Plan.ServerID).Scan(&allocatedCPU, &allocatedMem); err != nil {
			return err
		}
		if allocatedCPU+u.CPUMillis > cpu || allocatedMem+u.MemoryMiB > mem {
			return domain.Invalid("placement.capacity")
		}
	}
	return nil
}

func (s *Service) admitCrossHostApply(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p deployment.Plan) error {
	if p.Validate(true) != nil {
		return domain.Invalid("placement.plan")
	}
	n, err := readNetwork(ctx, tx, ws)
	if err != nil {
		return err
	}
	if n.ID != p.Distributed.NetworkID || n.Generation+1 != p.Distributed.NetworkGeneration || n.State != "ready" {
		return domain.Conflict
	}
	for _, u := range p.Distributed.Units {
		if _, err = s.visualServer(ctx, tx, ws, u.Plan.ServerID); err != nil {
			return err
		}
		var ready bool
		var pre []byte
		if err = tx.QueryRow(ctx, `SELECT NOT revoked AND NOT awaiting_transfer AND NOT preview_only AND role<>'build' AND COALESCE((capabilities->>'crossHost')::boolean,false) AND last_seen>now()-interval '45 seconds' AND NOT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$2 AND state NOT IN ('succeeded','failed','cancelled')) AND NOT EXISTS(SELECT 1 FROM cross_host_reservations WHERE server_id=$2),preflight FROM servers WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, u.Plan.ServerID).Scan(&ready, &pre); err != nil {
			return err
		}
		var host api.HostPreflight
		if !ready || json.Unmarshal(pre, &host) != nil || host.CrossHostInventory == nil || host.CrossHostInventory.UnboundedInventoryHash != u.InventoryHash {
			return domain.Invalid("placement.capacity")
		}
	}
	if err = checkCrossHostCapacity(ctx, tx, ws, p); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE workspace_networks SET generation=$2 WHERE workspace_id=$1`, ws, p.Distributed.NetworkGeneration); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE network_members SET generation=$2 WHERE workspace_id=$1`, ws, p.Distributed.NetworkGeneration); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE network_endpoints e SET retired=false FROM network_plan_endpoints p WHERE p.plan_id=$1 AND p.workspace_id=$2 AND e.id=p.endpoint_id AND e.workspace_id=$2`, p.ID, ws); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE network_endpoint_sources s SET active=true,ingress=(s.ingress OR p.ingress) FROM network_plan_sources p WHERE p.plan_id=$1 AND p.workspace_id=$2 AND s.endpoint_id=p.endpoint_id AND s.server_id=p.server_id AND s.unit_id=p.unit_id AND s.workspace_id=$2`, p.ID, ws); err != nil {
		return err
	}
	n, err = readNetwork(ctx, tx, ws)
	if err != nil {
		return err
	}
	for _, u := range p.Distributed.Units {
		found := false
		for _, m := range n.Members {
			if m.ServerID != u.Plan.ServerID {
				continue
			}
			manifest, ready, err := networkManifest(ctx, tx, ws, n, m)
			if err != nil {
				return err
			}
			if !ready || hostnetwork.Hash(manifest) != hostnetwork.Hash(u.Plan.Local.Network) {
				return domain.Conflict
			}
			found = true
		}
		if !found {
			return domain.Conflict
		}
	}
	return nil
}

func (s *Service) retireCrossHostGrants(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p deployment.Plan, abort bool) error {
	var keep *uuid.UUID
	if !abort {
		keep = &p.ID
	} else {
		var previous *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2`, ws, p.EnvironmentID).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		keep = previous
	}
	plans := []uuid.UUID{}
	if keep != nil {
		plans = append(plans, *keep)
	}
	return projectCrossHostGrants(ctx, tx, ws, p.EnvironmentID, plans)
}

func projectCrossHostGrants(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, plans []uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE network_endpoint_sources s SET active=EXISTS(SELECT 1 FROM network_plan_sources p WHERE p.plan_id=ANY($3::uuid[]) AND p.workspace_id=$1 AND p.endpoint_id=s.endpoint_id AND p.server_id=s.server_id AND p.unit_id=s.unit_id),ingress=EXISTS(SELECT 1 FROM network_plan_sources p WHERE p.plan_id=ANY($3::uuid[]) AND p.workspace_id=$1 AND p.endpoint_id=s.endpoint_id AND p.server_id=s.server_id AND p.unit_id=s.unit_id AND p.ingress) FROM network_endpoints e WHERE e.workspace_id=$1 AND e.environment_id=$2 AND s.workspace_id=$1 AND s.endpoint_id=e.id`, ws, env, plans); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE network_endpoints e SET retired=NOT EXISTS(SELECT 1 FROM network_plan_endpoints p WHERE p.plan_id=ANY($3::uuid[]) AND p.workspace_id=$1 AND p.endpoint_id=e.id) WHERE e.workspace_id=$1 AND e.environment_id=$2`, ws, env, plans); err != nil {
		return err
	}
	var generation int64
	if err := tx.QueryRow(ctx, `UPDATE workspace_networks SET generation=generation+1 WHERE workspace_id=$1 RETURNING generation`, ws).Scan(&generation); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE network_members SET generation=$2 WHERE workspace_id=$1`, ws, generation)
	return err
}
