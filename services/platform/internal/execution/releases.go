package execution

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
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

type releaseQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func requireReplicas(ctx context.Context, q releaseQuery, ws, server uuid.UUID, p deployment.Plan) error {
	if p.Recipe != deployment.ReplicaRecipe {
		return nil
	}
	var supported bool
	if err := q.QueryRow(ctx, "SELECT COALESCE((capabilities->>'replicas')::boolean,false) AND COALESCE((capabilities->>'safeReleases')::boolean,false) FROM servers WHERE workspace_id=$1 AND id=$2", ws, server).Scan(&supported); err != nil {
		return err
	}
	if !supported {
		return domain.Invalid("server.capabilities.replicas")
	}
	return nil
}

func releaseCapability(ctx context.Context, q releaseQuery, ws, server uuid.UUID) (bool, error) {
	var supported bool
	err := q.QueryRow(ctx, "SELECT COALESCE((capabilities->>'safeReleases')::boolean,false) FROM servers WHERE workspace_id=$1 AND id=$2", ws, server).Scan(&supported)
	return supported, err
}
func appliedVisualPlan(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID) (deployment.Plan, error) {
	var id *uuid.UUID
	err := tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && id == nil {
		return deployment.Plan{}, nil
	}
	if err != nil {
		return deployment.Plan{}, err
	}
	return readPlan(ctx, tx, ws, env, *id)
}
func configureRelease(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p *deployment.Plan) error {
	previous, err := appliedVisualPlan(ctx, tx, ws, p.EnvironmentID)
	if err != nil {
		return err
	}
	supported, err := releaseCapability(ctx, tx, ws, p.ServerID)
	if err != nil {
		return err
	}
	if len(p.Instances) > 0 && !supported {
		return domain.Invalid("server.capabilities.safeReleases")
	}
	app, eligible := deployment.ReleaseApplication(previous, *p)
	if !supported || eligible != nil {
		if p.ReleaseOptions != nil {
			return domain.Invalid("releaseOptions.eligibility")
		}
		return nil
	}
	options := deployment.DefaultReleaseOptions()
	if p.ReleaseOptions != nil {
		options = *p.ReleaseOptions
	}
	p.Release = &deployment.Release{ApplicationID: app.ID, PreviousPlanID: previous.ID, Options: options}
	p.ReplaceInstances(app.ID)
	p.Effects = []string{"Start an isolated stateless HTTP candidate; failed readiness vetoes the traffic switch and retains the serving revision.", "Verify the proxy switch, drain existing connections, then gracefully stop the old instance. Keep two previous instances while available.", "Image rollback restores application configuration only. Database writes and schema changes are never undone."}
	if p.Recipe == deployment.ReplicaRecipe {
		p.Effects[0] = "Start the complete stateless HTTP candidate group; every instance must pass readiness before traffic switches. Old and new groups temporarily run together."
		p.Effects[1] = "Verify all new private backends, drain existing connections, then stop the complete old group. Retain one stopped primary for each of two previous image generations."
	}
	if len(options.Command) > 0 {
		p.Effects = append(p.Effects, "Run the bounded release command once before readiness. An uncertain command outcome requires inspection; external side effects remain.")
	}
	return nil
}

type releaseTarget struct {
	Changes  []string  `json:"changes"`
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Image    string    `json:"image"`
	Eligible bool      `json:"eligible"`
	Note     string    `json:"note"`
}
type releaseHistory struct {
	PlanID       uuid.UUID       `json:"planId"`
	RevisionID   uuid.UUID       `json:"revisionId"`
	CreatedAt    time.Time       `json:"createdAt"`
	Applications []releaseTarget `json:"applications"`
}

func rollbackPlan(current, historical deployment.Plan, appID uuid.UUID) (deployment.Plan, error) {
	raw, _ := json.Marshal(current)
	var target deployment.Plan
	_ = json.Unmarshal(raw, &target)
	historical.HostChanges, historical.AcceptDrift = false, false
	if changed, err := deployment.ReleaseApplication(current, historical); err != nil || changed.ID != appID {
		return target, domain.Invalid("rollback.compatibility")
	}
	found := false
	for _, old := range historical.Specification.Applications {
		if old.ID == appID {
			for i, a := range target.Specification.Applications {
				if a.ID == appID {
					target.Specification.Applications[i] = old
					image := historical.Images[appID.String()]
					if old.Source.ArtifactID == nil {
						target.Specification.Applications[i].Source.Image = &image
					}
					target.Images[appID.String()] = image
					if artifact, ok := historical.Artifacts[appID.String()]; ok {
						if target.Artifacts == nil {
							target.Artifacts = map[string]pipeline.Artifact{}
						}
						target.Artifacts[appID.String()] = artifact
					} else {
						delete(target.Artifacts, appID.String())
					}
					found = true
				}
			}
		}
	}
	target.Release, target.ReleaseOptions = nil, nil
	target.HostChanges, target.AcceptDrift = false, false
	if !found || historical.ServerID != current.ServerID || historical.UnitID != current.UnitID || historical.EnvironmentID != current.EnvironmentID {
		return target, domain.Invalid("rollback.application")
	}
	changed, err := deployment.ReleaseApplication(current, target)
	if err != nil || changed.ID != appID {
		return target, domain.Invalid("rollback.compatibility")
	}
	return target, nil
}
func (s *Service) ReleaseHistory(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]releaseHistory, error) {
	out := []releaseHistory{}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		current, err := appliedVisualPlan(ctx, tx, ws, env)
		if err != nil {
			return err
		}
		supported, err := releaseCapability(ctx, tx, ws, current.ServerID)
		if err != nil && current.ID != uuid.Nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT p.plan,o.created_at FROM execution_operations o JOIN visual_plans p ON p.id=o.visual_plan_id WHERE o.workspace_id=$1 AND o.environment_id=$2 AND o.state='succeeded' AND o.command->'visual'->>'action'='deploy' ORDER BY o.created_at DESC,o.id DESC LIMIT 20`, ws, env)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var entry releaseHistory
			if err = rows.Scan(&raw, &entry.CreatedAt); err != nil {
				return err
			}
			var old deployment.Plan
			if err = json.Unmarshal(raw, &old); err != nil {
				return err
			}
			entry.PlanID, entry.RevisionID = old.ID, old.RevisionID
			entry.Applications = []releaseTarget{}
			for _, app := range old.Specification.Applications {
				_, compatible := rollbackPlan(current, old, app.ID)
				eligible := supported && compatible == nil && deployment.Hash(v.Spec) == deployment.Hash(current.Specification)
				note := "Requires an unchanged applied draft and a compatible stateless HTTP configuration."
				if eligible {
					note = "Create a rollback draft, then prepare a plan to check image availability and review effective configuration. No database rollback."
				}
				entry.Applications = append(entry.Applications, releaseTarget{Changes: releaseChanges(current, old, app.ID), ID: app.ID, Name: app.Name, Image: old.Images[app.ID.String()], Eligible: eligible, Note: note})
			}
			out = append(out, entry)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Service) RollbackRelease(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.RollbackRelease) (api.Revision, error) {
	var out api.Revision
	err := s.scope(ctx, p, ws, true, false, false, "release.rollback.draft", env, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Revision != in.BaseRevisionId {
			return domain.Conflict
		}
		current, err := appliedVisualPlan(ctx, tx, ws, env)
		if err != nil {
			return err
		}
		supported, err := releaseCapability(ctx, tx, ws, current.ServerID)
		if err != nil {
			return err
		}
		if !supported {
			return domain.Invalid("server.capabilities.safeReleases")
		}
		if deployment.Hash(v.Spec) != deployment.Hash(current.Specification) {
			return domain.Conflict
		}
		var applied bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND visual_plan_id=$3 AND state='succeeded')", ws, env, id).Scan(&applied)
		if err != nil {
			return err
		}
		if !applied {
			return domain.Invalid("rollback.plan")
		}
		old, err := readPlan(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		target, err := rollbackPlan(current, old, in.ApplicationId)
		if err != nil {
			return err
		}
		revision, err := s.writePipelineRevision(ctx, tx, ws, env, v.Revision, p.Account.ID, target.Specification)
		if err != nil {
			return err
		}
		row, err := db.New(tx).GetRevision(ctx, db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: revision})
		if err != nil {
			return err
		}
		out = api.Revision{Id: row.ID, AuthorId: row.AuthorID, CreatedAt: row.CreatedAt, Hash: hex.EncodeToString(row.ContentHash), Specification: json.RawMessage(row.Specification)}
		return nil
	})
	return out, err
}

func releaseChanges(current, old deployment.Plan, id uuid.UUID) []string {
	var a, b domain.Application
	for _, app := range current.Specification.Applications {
		if app.ID == id {
			a = app
		}
	}
	for _, app := range old.Specification.Applications {
		if app.ID == id {
			b = app
		}
	}
	changes := []string{}
	for _, field := range []struct {
		name         string
		current, old any
	}{
		{"Image digest", current.Images[id.String()], old.Images[id.String()]},
		{"Source/artifact reference", a.Source, b.Source}, {"Name", a.Name, b.Name},
		{"Variables and secret-version references", a.Variables, b.Variables},
		{"Startup command", a.Command, b.Command}, {"HTTP readiness", a.Health, b.Health},
		{"Dependencies", a.DependsOn, b.DependsOn}, {"Networks", a.Networks, b.Networks},
		{"Replica count", a.ReplicaCount(), b.ReplicaCount()},
	} {
		if deployment.Hash(field.current) != deployment.Hash(field.old) {
			changes = append(changes, field.name)
		}
	}
	return changes
}
