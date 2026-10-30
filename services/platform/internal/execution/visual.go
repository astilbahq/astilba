package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type visualEnvironment struct {
	Purpose         string
	Unit, Revision  uuid.UUID
	Server, Applied *uuid.UUID
	Spec            domain.Specification
}

func loadVisualEnvironment(ctx context.Context, tx pgx.Tx, ws, pid, id uuid.UUID) (visualEnvironment, error) {
	var v visualEnvironment
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT d.id,d.server_id,e.draft_revision_id,e.applied_revision_id,r.specification,e.purpose FROM environments e JOIN deployment_units d ON d.workspace_id=e.workspace_id AND d.environment_id=e.id AND d.primary_unit JOIN specification_revisions r ON r.workspace_id=e.workspace_id AND r.environment_id=e.id AND r.id=e.draft_revision_id WHERE e.workspace_id=$1 AND e.project_id=$2 AND e.id=$3`, ws, pid, id).Scan(&v.Unit, &v.Server, &v.Revision, &v.Applied, &raw, &v.Purpose)
	if err == nil {
		v.Spec, _, err = domain.ParseSpec(raw)
	}
	return v, err
}
func (s *Service) visualServer(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) ([]int, error) {
	if err := s.deploymentServer(ctx, tx, ws, id); err != nil {
		return nil, err
	}
	var cap api.AgentCapabilities
	var cb, pb []byte
	err := tx.QueryRow(ctx, "SELECT capabilities,ingress_ports FROM servers WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&cb, &pb)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(cb, &cap) != nil || cap.Visual == nil || !*cap.Visual {
		return nil, domain.Invalid("server.capabilities.visual")
	}
	var ports []int
	err = json.Unmarshal(pb, &ports)
	return ports, err
}
func requiredPorts(spec domain.Specification) []int {
	ports := []int{}
	seen := map[int]bool{}
	for _, r := range spec.Routes {
		values := []int{80, 443}
		if r.Protocol == "tcp" {
			values = []int{80, 443, r.ListenPort}
		}
		for _, p := range values {
			if !seen[p] {
				ports = append(ports, p)
				seen[p] = true
			}
		}
	}
	return ports
}
func (s *Service) CreateVisualPlan(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, in api.VisualPlanRequest) (deployment.Status, error) {
	var out deployment.Status
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: in.AllowHostChanges, Recent: in.AllowHostChanges}, "deployment.plan", env, func(tx pgx.Tx) error {
		var err error
		out, err = s.createVisualPlan(ctx, tx, ws, pid, env, in)
		return err
	})
	return out, err
}
func (s *Service) createVisualPlan(ctx context.Context, tx pgx.Tx, ws, pid, env uuid.UUID, in api.VisualPlanRequest) (deployment.Status, error) {
	var out deployment.Status
	err := func() error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Revision != in.BaseRevisionId || (v.Server != nil && *v.Server != in.ServerId) {
			return domain.Conflict
		}
		var imported *uuid.UUID
		err = tx.QueryRow(ctx, "SELECT imported_revision_id FROM git_sources WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&imported)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			baseline, err := sourceBaseline(ctx, tx, ws, env, imported)
			if err != nil {
				return err
			}
			if baseline == nil || !sameSourceFiles(v.Spec.Compose, baseline) {
				return domain.Invalid("source.awaitingMerge")
			}
		}
		if err = s.Store.ValidateApplicationRoutes(ctx, tx, ws, env, v.Spec, &in.ServerId); err != nil {
			return err
		}
		ports, err := s.visualServer(ctx, tx, ws, in.ServerId)
		if err != nil {
			return err
		}
		if err = requireServiceOutputs(ctx, tx, ws, in.ServerId, v.Spec); err != nil {
			return err
		}
		if v.Spec.Compose != nil {
			var supported bool
			if err := tx.QueryRow(ctx, "SELECT COALESCE((capabilities->>'compose')::boolean,false) FROM servers WHERE workspace_id=$1 AND id=$2", ws, in.ServerId).Scan(&supported); err != nil {
				return err
			}
			if !supported {
				return domain.Invalid("server.capabilities.compose")
			}
		}
		known := map[int]bool{}
		for _, port := range ports {
			known[port] = true
		}
		hostChanges := in.AllowHostChanges
		for _, port := range requiredPorts(v.Spec) {
			hostChanges = hostChanges || !known[port]
		}
		if hostChanges && !in.AllowHostChanges {
			return domain.Invalid("allowHostChanges")
		}
		if v.Spec.Compose != nil {
			source, err := deployment.SourceProject(deployment.Plan{Specification: v.Spec}, nil)
			if err != nil {
				return err
			}
			if len(source.Permissions) > 0 {
				if !in.AllowHostChanges {
					return domain.Invalid("allowHostChanges")
				}
				hostChanges = true
			}
		}
		if err := s.validateVisualSecrets(ctx, tx, ws, env, v.Spec); err != nil {
			return err
		}
		previous := domain.Specification{}
		if v.Applied != nil {
			var raw []byte
			if err := tx.QueryRow(ctx, "SELECT specification FROM specification_revisions WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, v.Applied).Scan(&raw); err != nil {
				return err
			}
			if json.Unmarshal(raw, &previous) != nil {
				return errors.New("invalid applied specification")
			}
		}
		if err = deployment.PostgresVersionChange(previous, v.Spec); err != nil {
			return domain.Invalid("applications.postgres.upgrade")
		}
		for _, a := range v.Spec.Applications {
			if a.Postgres == nil {
				continue
			}
			var reserved *string
			err := tx.QueryRow(ctx, "SELECT postgres_init_hash FROM managed_volumes WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, a.Mounts[0].VolumeID).Scan(&reserved)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if reserved != nil && *reserved != deployment.Hash(a.Postgres) {
				return domain.Invalid("applications.postgres.existingVolume")
			}
		}
		plan := deployment.Plan{ID: uuid.New(), ServerID: in.ServerId, EnvironmentID: env, UnitID: v.Unit, RevisionID: v.Revision, ExpectedAppliedID: v.Applied, Recipe: deployment.Recipe, Specification: v.Spec, Images: map[string]string{}, HostChanges: hostChanges, AcceptDrift: in.AcceptDrift, Effects: deployment.Effects(v.Spec, previous, hostChanges), ExpiresAt: time.Now().UTC().Add(15 * time.Minute)}.WithHash()
		if v.Purpose == "preview" {
			plan.Recipe = deployment.PreviewRecipe
			plan.Preview, err = s.previewPlanIdentity(ctx, tx, ws, env)
			if err != nil {
				return err
			}
		}
		if err = s.planArtifacts(ctx, tx, ws, &plan); err != nil {
			return err
		}

		previousPlan, err := appliedVisualPlan(ctx, tx, ws, env)
		if err != nil {
			return err
		}
		if v.Spec.Placement != nil {
			out, err = s.createCrossHostPlan(ctx, tx, ws, pid, env, in, v, previousPlan)
			return err
		}
		if previousPlan.Distributed != nil {
			if previousPlan.Specification.HasRemotePlacement() {
				return domain.Invalid("placement.consolidateFirst")
			}
			if !in.AllowHostChanges {
				return domain.Invalid("allowHostChanges")
			}
			for _, unit := range previousPlan.Distributed.Units {
				if unit.Plan.Local.Primary {
					previousPlan = unit.Plan
					break
				}
			}
			plan.HostChanges = true
			plan.Effects = append(plan.Effects, "Remove the consolidated placement configuration and private port publications. Persistent services can restart on their existing volumes.")
		}
		plan.PrepareInstances(previousPlan)
		if err = requireReplicas(ctx, tx, ws, plan.ServerID, plan); err != nil {
			return err
		}
		if in.ReleaseOptions != nil {
			plan.ReleaseOptions = &deployment.ReleaseOptions{Command: in.ReleaseOptions.Command, CommandTimeoutSeconds: in.ReleaseOptions.CommandTimeoutSeconds, DrainSeconds: in.ReleaseOptions.DrainSeconds}
		}
		supported, err := releaseCapability(ctx, tx, ws, plan.ServerID)
		if err != nil {
			return err
		}
		if (len(plan.Instances) > 0 || plan.ReleaseOptions != nil) && !supported {
			return domain.Invalid("server.capabilities.safeReleases")
		}
		plan = plan.WithHash()
		if v.Spec.Compose != nil {
			source, _ := deployment.SourceProject(plan, nil)
			plan.Effects = append(plan.Effects, source.Permissions...)
			plan = plan.WithHash()
		}
		if err := plan.Validate(false); err != nil {
			return domain.Invalid("specification")
		}
		op, err := s.insertVisual(ctx, tx, ws, uuid.New(), deployment.Payload{Action: "resolve", Plan: plan})
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(plan)
		if len(raw) > 100000 {
			return domain.Invalid("specification")
		}
		_, err = tx.Exec(ctx, `INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, plan.ID, ws, in.ServerId, env, v.Unit, v.Revision, op.Id, raw)
		out = deployment.Status{ID: plan.ID, OperationID: op.Id, State: string(op.State)}
		return err
	}()
	return out, err
}

func (s *Service) insertVisual(ctx context.Context, tx pgx.Tx, ws, key uuid.UUID, payload deployment.Payload) (api.Operation, error) {
	var generation int64
	if err := tx.QueryRow(ctx, "UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation", ws, payload.Plan.EnvironmentID).Scan(&generation); err != nil {
		return api.Operation{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return api.Operation{}, err
	}
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: payload.Plan.ServerID, EnvironmentId: payload.Plan.EnvironmentID, ResourceId: payload.Plan.UnitID, Generation: generation, Kind: "visual", Action: "inspect", PlanHash: payload.Digest(), Deadline: time.Now().Add(30 * time.Minute), Visual: rawMessage(raw)}
	return s.insert(ctx, tx, ws, key, nil, c)
}
func (s *Service) VisualPlan(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID) (deployment.Status, error) {
	var out deployment.Status
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT r.id,r.operation_id,o.state,p.plan FROM visual_requests r JOIN execution_operations o ON o.id=r.operation_id LEFT JOIN visual_plans p ON p.id=r.id WHERE r.workspace_id=$1 AND r.environment_id=$2 AND r.id=$3`, ws, env, id).Scan(&out.ID, &out.OperationID, &out.State, &raw)
		if err == nil && raw != nil {
			var plan deployment.Plan
			if err = json.Unmarshal(raw, &plan); err == nil {
				out.Plan = &plan
				out.State = "ready"
			}
		}
		return err
	})
	return out, err
}
func readPlan(ctx context.Context, tx pgx.Tx, ws, env, id uuid.UUID) (deployment.Plan, error) {
	var p deployment.Plan
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT plan FROM visual_plans WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, id).Scan(&raw)
	if err == nil {
		if json.Unmarshal(raw, &p) != nil {
			err = domain.Invalid("deployment.plan")
		}
	}
	return p, err
}
func (s *Service) ApplyVisualPlan(ctx context.Context, p store.Principal, ws, pid, env, id, key uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true}, "deployment.apply", id, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(ctx, "SELECT role FROM memberships WHERE workspace_id=$1 AND account_id=$2", ws, p.Account.ID).Scan(&role); err != nil {
			return err
		}
		plan, err := readPlan(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		if plan.HostChanges {
			if role != "owner" {
				return domain.Forbidden
			}
			if !p.Recent {
				return domain.RecentRequired
			}
		}
		out, err = s.applyVisualPlan(ctx, tx, ws, pid, env, id, key, role == "owner" && p.Recent)
		return err
	})
	return out, err
}
func (s *Service) applyVisualPlan(ctx context.Context, tx pgx.Tx, ws, pid, env, id, key uuid.UUID, allowHost bool) (api.Operation, error) {
	var out api.Operation
	err := func() error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		var existing *uuid.UUID
		err = tx.QueryRow(ctx, "SELECT visual_plan_id FROM execution_operations WHERE workspace_id=$1 AND request_key=$2", ws, key).Scan(&existing)
		if err == nil {
			if existing == nil || *existing != id {
				return domain.Conflict
			}
			out, err = scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND request_key=$2", ws, key))
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		plan, err := readPlan(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		if time.Now().After(plan.ExpiresAt) || plan.RevisionID != v.Revision || plan.UnitID != v.Unit || (v.Server != nil && *v.Server != plan.ServerID) || !sameID(v.Applied, plan.ExpectedAppliedID) {
			return domain.Conflict
		}
		if err = s.Store.ValidateApplicationRoutes(ctx, tx, ws, env, plan.Specification, &plan.ServerID); err != nil {
			return err
		}
		if plan.HostChanges && !allowHost {
			return domain.RecentRequired
		}
		if _, err = s.visualServer(ctx, tx, ws, plan.ServerID); err != nil {
			return err
		}
		if err = requireServiceOutputs(ctx, tx, ws, plan.ServerID, plan.Specification); err != nil {
			return err
		}
		if plan.Distributed != nil {
			if plan.Restore != nil {
				err = s.admitRestoreApply(ctx, tx, ws, plan)
			} else {
				err = s.admitCrossHostApply(ctx, tx, ws, plan)
			}
			if err != nil {
				return err
			}
		}
		if err = requireReplicas(ctx, tx, ws, plan.ServerID, plan); err != nil {
			return err
		}
		if len(plan.Instances) > 0 || plan.Release != nil {
			supported, err := releaseCapability(ctx, tx, ws, plan.ServerID)
			if err != nil {
				return err
			}
			if !supported {
				return domain.Invalid("server.capabilities.safeReleases")
			}
		}
		// Claims include previously applied routes until a successful replacement.
		claims, err := deployment.Claims(plan)
		if err != nil {
			return domain.Invalid("routes")
		}
		for _, claim := range claims {
			var owner uuid.UUID
			err = tx.QueryRow(ctx, `INSERT INTO route_claims(server_id,claim,workspace_id,environment_id) VALUES($1,$2,$3,$4) ON CONFLICT(server_id,claim) DO UPDATE SET claim=EXCLUDED.claim RETURNING environment_id`, plan.ServerID, claim, ws, env).Scan(&owner)
			if err != nil {
				return err
			}
			if owner != env {
				return domain.Conflict
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE deployment_units SET server_id=$3 WHERE workspace_id=$1 AND id=$2", ws, v.Unit, plan.ServerID); err != nil {
			return err
		}
		for _, a := range plan.Specification.Applications {
			var owner uuid.UUID
			err = tx.QueryRow(ctx, `INSERT INTO managed_resources(workspace_id,environment_id,unit_id,id) VALUES($1,$2,$3,$4) ON CONFLICT(workspace_id,id) DO UPDATE SET id=EXCLUDED.id RETURNING environment_id`, ws, env, v.Unit, a.ID).Scan(&owner)
			if err != nil {
				return err
			}
			if owner != env {
				return domain.Conflict
			}
		}
		for _, vol := range plan.Specification.Volumes {
			var owner uuid.UUID
			var deleted bool
			var initial, stored *string
			for _, app := range plan.Specification.Applications {
				if app.Postgres != nil && app.Mounts[0].VolumeID == vol.ID {
					value := deployment.Hash(app.Postgres)
					initial = &value
				}
			}
			err = tx.QueryRow(ctx, `INSERT INTO managed_volumes(workspace_id,environment_id,unit_id,id,postgres_init_hash) VALUES($1,$2,$3,$4,$5) ON CONFLICT(workspace_id,id) DO UPDATE SET id=EXCLUDED.id RETURNING environment_id,deleted,postgres_init_hash`, ws, env, v.Unit, vol.ID, initial).Scan(&owner, &deleted, &stored)
			if err != nil {
				return err
			}
			if owner != env || deleted {
				return domain.Conflict
			}
			if initial != nil {
				if stored != nil && *stored != *initial {
					return domain.Invalid("applications.postgres.existingVolume")
				}
				if stored == nil {
					if _, err = tx.Exec(ctx, "UPDATE managed_volumes SET postgres_init_hash=$3 WHERE workspace_id=$1 AND id=$2", ws, vol.ID, initial); err != nil {
						return err
					}
				}
			}
		}
		if plan.Restore != nil {
			out, err = s.insertRestoreMaintenance(ctx, tx, ws, key, plan)
		} else if plan.Distributed != nil {
			out, err = s.insertCoordination(ctx, tx, ws, key, "deploy", plan)
		} else {
			out, err = s.insertVisual(ctx, tx, ws, key, deployment.Payload{Action: "deploy", Plan: plan})
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE execution_operations SET visual_plan_id=$2 WHERE id=$1", out.Id, id)
		return err
	}()
	return out, err
}

func sameID(a, b *uuid.UUID) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (s *Service) Runtime(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) (deployment.Observation, error) {
	out := deployment.Observation{Resources: []deployment.Resource{}}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, "SELECT observation FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		unavailable := map[uuid.UUID]bool{}
		for i := range out.Hosts {
			host := &out.Hosts[i]
			var online bool
			var phase string
			if err := tx.QueryRow(ctx, `SELECT NOT revoked AND NOT awaiting_transfer AND last_seen>now()-interval '45 seconds',COALESCE((SELECT CASE WHEN o.command->'crosshost'->>'action'='observe' THEN 'inspection pending' WHEN o.command->'crosshost' ? 'maintenance' THEN 'maintenance' ELSE 'changing' END FROM cross_host_reservations r JOIN execution_operations o ON o.id=r.parent_id WHERE r.server_id=$2 AND o.environment_id=$3 AND o.state NOT IN ('succeeded','failed','cancelled')),'') FROM servers WHERE workspace_id=$1 AND id=$2`, ws, host.ServerID, env).Scan(&online, &phase); err != nil {
				return err
			}
			if !online {
				host.State = "unknown"
				unavailable[host.ServerID] = true
			} else {
				host.State = "snapshot"
				if phase != "" {
					host.State = phase
					unavailable[host.ServerID] = true
				}
			}
		}
		for i := range out.Resources {
			resource := &out.Resources[i]
			for j := range resource.Instances {
				instance := &resource.Instances[j]
				if instance.ServerID != nil && unavailable[*instance.ServerID] {
					instance.State = "unknown"
					resource.State = "unknown"
				}
			}
		}
		return nil

	})
	return out, err
}
func (s *Service) ObserveVisual(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, volume *uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: volume != nil, Recent: volume != nil}, "deployment.observe", env, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Server == nil {
			return domain.Invalid("server")
		}
		if _, err = s.visualServer(ctx, tx, ws, *v.Server); err != nil {
			return err
		}
		var id uuid.UUID
		err = tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND plan_id IS NOT NULL", ws, env).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, "SELECT visual_plan_id FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND visual_plan_id IS NOT NULL ORDER BY created_at DESC LIMIT 1", ws, env).Scan(&id)
		}
		if err != nil {
			return err
		}
		plan, err := readPlan(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		if plan.Distributed != nil {
			if volume == nil {
				out, err = s.insertCoordination(ctx, tx, ws, uuid.New(), "observe", plan)
				return err
			}
			plan = plan.PrimaryPlan()
		}
		action := "observe"
		if volume != nil {
			action = "delete-volume"
			var protected bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supported_upgrades WHERE workspace_id=$1 AND environment_id=$2 AND (plan->>'targetVolumeId'=$3 OR EXISTS(SELECT 1 FROM jsonb_array_elements(plan->'source'->'specification'->'applications') a, jsonb_array_elements(a->'mounts') m WHERE a->>'id'=plan->>'resourceId' AND m->>'volumeId'=$3)) AND (status->>'phase' NOT IN ('planned','rejected','complete','returned') OR (status->>'keepUntil')::timestamptz>now()))`, ws, env, volume.String()).Scan(&protected); err != nil {
				return err
			}
			if protected {
				return domain.Conflict
			}
			var ok bool
			if err := tx.QueryRow(ctx, "SELECT true FROM managed_volumes WHERE workspace_id=$1 AND environment_id=$2 AND unit_id=$3 AND id=$4 AND NOT deleted", ws, env, v.Unit, *volume).Scan(&ok); err != nil {
				return err
			}
			for _, a := range v.Spec.Applications {
				for _, m := range a.Mounts {
					if m.VolumeID == *volume {
						return domain.Conflict
					}
				}
			}
		}
		out, err = s.insertVisual(ctx, tx, ws, uuid.New(), deployment.Payload{Action: action, Plan: plan, VolumeID: volume})
		return err
	})
	return out, err
}

// Secret values are hydrated only at the authenticated agent transport boundary.
func (s *Service) hydrateVisual(ctx context.Context, c *api.ExecutionCommand) error {
	if c.Visual != nil {
		var p deployment.Payload
		if json.Unmarshal(*c.Visual, &p) == nil && p.Action == "preview-cleanup" {
			return nil
		}
	}
	if c.Visual == nil {
		return errors.New("missing visual payload")
	}
	var payload deployment.Payload
	if json.Unmarshal(*c.Visual, &payload) != nil {
		return errors.New("invalid visual payload")
	}

	payload.Secrets = map[string]string{}
	var ws uuid.UUID
	if err := s.Store.Pool.QueryRow(ctx, "SELECT workspace_id FROM servers WHERE id=$1", c.ServerId).Scan(&ws); err != nil {
		return err
	}
	if err := requireServiceOutputs(ctx, s.Store.Pool, ws, c.ServerId, payload.Plan.Specification); err != nil {
		return err
	}
	if err := requireReplicas(ctx, s.Store.Pool, ws, c.ServerId, payload.Plan); err != nil {
		return err
	}
	if len(payload.Plan.Instances) > 0 || payload.Plan.Release != nil {
		supported, err := releaseCapability(ctx, s.Store.Pool, ws, c.ServerId)
		if err != nil {
			return err
		}
		if !supported {
			return domain.Invalid("server.capabilities.safeReleases")
		}
	}
	payload.RegistryAccess = map[string]pipeline.Credential{}
	for key, a := range payload.Plan.Artifacts {
		if a.RegistryID == nil {
			continue
		}
		var cipher []byte
		if err := s.Store.Pool.QueryRow(ctx, "SELECT encrypted_access FROM registries WHERE workspace_id=$1 AND id=$2", ws, *a.RegistryID).Scan(&cipher); err != nil {
			return err
		}
		var access pipeline.RegistryAccess
		if err := s.openIntegration(ws, *a.RegistryID, "registry", cipher, &access); err != nil {
			return err
		}
		payload.RegistryAccess[key] = access.Pull
	}
	total := 0
	refs := payload.Plan.SecretReferences()
	if payload.Action == "resolve" {
		refs = nil
	}
	for _, id := range refs {
		v, err := s.Store.Queries.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: c.EnvironmentId, ID: id})
		if err != nil {
			return err
		}
		plain, err := s.Store.Keys.Open(v.EncryptedValue, store.SecretIdentity(ws, id))
		if err != nil {
			return err
		}
		total += len(plain)
		if total > 65536 {
			return errors.New("execution secret budget exceeded")
		}
		payload.Secrets[id.String()] = string(plain)
	}
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) > 250000 {
		return errors.New("execution payload limit exceeded")
	}
	c.Visual = rawMessage(raw)
	return nil
}

func (s *Service) visualReport(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, r *api.AgentReport) error {
	if c.Visual != nil {
		var p deployment.Payload
		if json.Unmarshal(*c.Visual, &p) == nil && p.Action == "preview-cleanup" {
			if r.State != "succeeded" {
				return nil
			}
			var ws uuid.UUID
			if err := tx.QueryRow(ctx, "SELECT workspace_id FROM servers WHERE id=$1", c.ServerId).Scan(&ws); err != nil {
				return err
			}
			return s.finalizePreviewCleanup(ctx, tx, ws, c)
		}
	}
	if c.Visual == nil || r.Observation == nil {
		if r.State != "succeeded" {
			return nil
		}
		return domain.Invalid("observation")
	}
	var payload deployment.Payload
	var observed deployment.Observation
	if json.Unmarshal(*c.Visual, &payload) != nil || json.Unmarshal(*r.Observation, &observed) != nil || len(observed.Resources) > 40 || observed.OmittedResources < 0 || len(*r.Observation) > deployment.ObservationLimit {
		return domain.Invalid("observation")
	}
	plan := payload.Plan
	if plan.ServerID != c.ServerId || plan.EnvironmentID != c.EnvironmentId || payload.Digest() != c.PlanHash {
		return domain.Forbidden
	}
	var ws uuid.UUID
	if err := tx.QueryRow(ctx, "SELECT workspace_id FROM servers WHERE id=$1", c.ServerId).Scan(&ws); err != nil {
		return err
	}
	allowed := map[uuid.UUID]bool{}
	for _, a := range plan.Specification.Applications {
		allowed[a.ID] = true
	}
	rows, err := tx.Query(ctx, "SELECT id FROM managed_resources WHERE workspace_id=$1 AND environment_id=$2 AND unit_id=$3", ws, c.EnvironmentId, plan.UnitID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		allowed[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	seen := map[uuid.UUID]bool{}
	for _, v := range observed.Resources {
		if !allowed[v.ID] || seen[v.ID] || len(v.Logs) > 2048 || len(v.Note) > 256 || len(v.Image) > 512 || len(v.Instances) > domain.MaxReplicas {
			return domain.Invalid("observation.resources")
		}
		members := map[uuid.UUID]bool{}
		for _, instance := range v.Instances {
			if instance.InstanceID == uuid.Nil || members[instance.InstanceID] || len(instance.State) > 30 {
				return domain.Invalid("observation.instances")
			}
			members[instance.InstanceID] = true
		}
		seen[v.ID] = true
	}
	if payload.Action == "resolve" && r.State == "succeeded" {
		plan.Images = observed.Images
		plan.ExpectedState = observed.StateHash
		if observed.InfrastructureDrift && !plan.AcceptDrift {
			r.State = "failed"
			r.Note = "Ingress drift requires an explicit reconciliation plan."
			return nil
		}
		if observed.ResourceDrift && !plan.AcceptDrift {
			r.State = "failed"
			r.Note = "Host drift was detected. Review it and explicitly request a reconciliation plan."
			return nil
		}
		if err := configureRelease(ctx, tx, ws, &plan); err != nil {
			r.State = "failed"
			r.Note = "Protected release is unavailable for this resolved change. Review its compatibility and agent capabilities."
			return nil
		}
		plan = plan.WithHash()
		if plan.Validate(true) != nil {
			return domain.Invalid("images")
		}
		raw, _ := json.Marshal(plan)
		_, err := tx.Exec(ctx, "INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING", plan.ID, ws, c.EnvironmentId, raw)
		return err
	}

	if observed.ObservedAt.IsZero() || observed.ObservedAt.After(time.Now().Add(time.Minute)) {
		return domain.Invalid("observedAt")
	}
	var previous *uuid.UUID
	err = tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2", ws, c.EnvironmentId).Scan(&previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if payload.Action == "deploy" && observed.Applied && r.State == "succeeded" {
		if observed.OmittedResources != 0 || observed.ResourceDrift || len(observed.Resources) != len(plan.Specification.Applications) {
			return domain.Invalid("observation.resources")
		}
		for _, a := range plan.Specification.Applications {
			if !seen[a.ID] {
				return domain.Invalid("observation.resources")
			}
		}
		for _, resource := range observed.Resources {
			if resource.Drift || (resource.State != "running" && resource.State != "healthy") || resource.Image != plan.Images[resource.ID.String()] {
				return domain.Invalid("observation.resources")
			}
			if plan.Recipe == deployment.ReplicaRecipe {
				wanted := plan.InstanceIDs(resource.ID)
				if len(resource.Instances) != len(wanted) {
					return domain.Invalid("observation.instances")
				}
				members := map[uuid.UUID]bool{}
				for _, id := range wanted {
					members[id] = true
				}
				for _, instance := range resource.Instances {
					if !members[instance.InstanceID] || instance.State != "running" && instance.State != "healthy" {
						return domain.Invalid("observation.instances")
					}
				}
			}
		}
		if observed.InfrastructureDrift {
			return domain.Invalid("observation.infrastructureDrift")
		}
		if plan.Distributed == nil && plan.Local == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM cross_host_runtime WHERE workspace_id=$1 AND environment_id=$2`, ws, c.EnvironmentId); err != nil {
				return err
			}
		}
		previous = &plan.ID
		if _, err := tx.Exec(ctx, "UPDATE environments SET applied_revision_id=$3 WHERE workspace_id=$1 AND id=$2", ws, c.EnvironmentId, plan.RevisionID); err != nil {
			return err
		}
		claims, err := deployment.Claims(plan)
		if err != nil {
			return domain.Invalid("routes")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM route_claims WHERE server_id=$1 AND workspace_id=$2 AND environment_id=$3 AND NOT (claim=ANY($4::text[]))", c.ServerId, ws, c.EnvironmentId, claims); err != nil {
			return err
		}
		ports := requiredPorts(plan.Specification)
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT ingress_ports FROM servers WHERE id=$1", c.ServerId).Scan(&raw); err != nil {
			return err
		}
		var old []int
		if json.Unmarshal(raw, &old) != nil {
			return errors.New("invalid server ports")
		}
		knownPorts := map[int]bool{}
		for _, p := range old {
			knownPorts[p] = true
		}
		for _, p := range ports {
			knownPorts[p] = true
		}
		old = []int{}
		for p := range knownPorts {
			old = append(old, p)
		}
		sort.Ints(old)
		raw, _ = json.Marshal(old)
		if _, err := tx.Exec(ctx, "UPDATE servers SET ingress_ports=$2 WHERE id=$1", c.ServerId, raw); err != nil {
			return err
		}
	}
	if payload.Action == "delete-volume" && r.State == "succeeded" && payload.VolumeID != nil {
		if _, err := tx.Exec(ctx, "UPDATE managed_volumes SET deleted=true WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, c.EnvironmentId, *payload.VolumeID); err != nil {
			return err
		}
	}
	if plan.Local != nil && payload.Action == "delete-volume" {
		// Removing an unused primary volume must not replace the group snapshot
		// with a primary-only observation.
		return nil
	}
	observed.Images = nil
	raw, _ := json.Marshal(observed)
	_, err = tx.Exec(ctx, `INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,observation) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,environment_id) DO UPDATE SET observation=EXCLUDED.observation,observed_at=now(),plan_id=EXCLUDED.plan_id`, ws, c.EnvironmentId, plan.UnitID, c.ServerId, previous, raw)
	return err
}

func rawMessage(raw []byte) *json.RawMessage { v := json.RawMessage(raw); return &v }

func (s *Service) validateVisualSecrets(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, spec domain.Specification) error {
	if len(domain.SecretReferences(spec)) > 100 {
		return domain.Invalid("secrets")
	}
	total, encodedTotal := 0, 0
	for _, id := range domain.SecretReferences(spec) {
		secret, err := db.New(tx).GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
			return domain.Invalid("secretVersionId")
		}
		plain, err := s.Store.Keys.Open(secret.EncryptedValue, store.SecretIdentity(ws, id))
		if err != nil {
			return err
		}
		total += len(plain)
		escaped, _ := json.Marshal(string(plain))
		encodedTotal += len(escaped)
	}
	if total > 65536 || encodedTotal > 140000 {
		return domain.Invalid("secrets")
	}
	return nil
}
