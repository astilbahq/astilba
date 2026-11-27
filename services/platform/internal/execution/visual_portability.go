package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) CloneVisual(ctx context.Context, p store.Principal, ws, pid, source uuid.UUID, in api.CloneEnvironment) (db.GetEnvironmentRow, error) {
	var out db.GetEnvironmentRow
	name, err := domain.Name(in.Name)
	if err != nil {
		return out, err
	}
	// A clone stores new secret versions for generated database passwords, and with
	// CopySecrets also opens the source's, which its audit action says.
	purpose, action := secretbroker.Seal, "environment.clone"
	if in.CopySecrets {
		purpose, action = secretbroker.Copy, "environment.clone_with_secrets"
	}
	access := workspaceAccess{Write: true, Recent: in.CopySecrets}
	keys, err := s.Store.BeginKeys(ctx, p, access.admission(), false, secretbroker.Request{Workspace: ws, Purpose: purpose, Kinds: []secretbroker.Kind{secretbroker.SecretVersion}})
	if err != nil {
		return out, err
	}
	used := false
	defer func() { keys.Close(ctx, secretbroker.UsedIf(used)) }()
	err = s.scope(ctx, p, ws, access, action, source, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, source)
		if err != nil {
			return err
		}
		if v.Purpose != "standard" {
			return domain.Invalid("preview.cloneUnsupported")
		}
		// A clone can copy secret values, which nothing may use while the source is not active.
		if err = requireActive(v.State); err != nil {
			return err
		}
		if v.Spec.Compose != nil && len(v.Spec.Routes) > 0 {
			return &domain.Error{Code: "invalid_input", Message: "Compose sources with routes cannot be cloned automatically. Create a Compose environment and copy the bundle with new routes.", Fields: []string{"compose.routes"}}
		}
		if v.Spec.Compose != nil && !in.CopySecrets && len(domain.SecretReferences(v.Spec)) > 0 {
			return domain.Invalid("compose.clone.copySecrets")
		}
		env, rev, unit := uuid.New(), uuid.New(), uuid.New()
		q := db.New(tx)
		if _, err = q.InsertEnvironment(ctx, db.InsertEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: env, Name: name, DraftRevisionID: rev, ConfigurationOwner: func() string {
			if v.Spec.Compose != nil {
				return "compose"
			}
			return "dashboard"
		}()}); err != nil {
			return err
		}
		// The source's routes as they stand, before they take the clone's identities.
		routes := slices.Clone(v.Spec.Routes)
		ids := map[uuid.UUID]uuid.UUID{}
		for _, a := range v.Spec.Applications {
			ids[a.ID] = uuid.New()
		}
		for _, vol := range v.Spec.Volumes {
			ids[vol.ID] = uuid.New()
		}
		for _, b := range v.Spec.Bindings {
			ids[b.ID] = uuid.New()
		}
		for _, route := range v.Spec.Routes {
			ids[route.ID] = uuid.New()
		}
		secrets := map[uuid.UUID]uuid.UUID{}
		create := func(label, value string) (uuid.UUID, error) {
			id := uuid.New()
			err := store.InsertSecretValue(ctx, q, keys, p, ws, env, id, label, value)
			used = used || err == nil
			return id, err
		}
		if in.CopySecrets {
			for _, old := range domain.SecretReferences(v.Spec) {
				secret, err := q.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: source, ID: old})
				if err != nil {
					return err
				}
				plain, err := keys.Open(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: old, Environment: source}, secret.EncryptedValue)
				if err != nil {
					return err
				}
				id, err := create("Copied "+old.String(), string(plain))
				if err != nil {
					return err
				}
				secrets[old] = id
			}
		}
		variables := func(values []domain.Variable) []domain.Variable {
			out := []domain.Variable{}
			for _, value := range values {
				if value.SecretVersionID != nil {
					id, ok := secrets[*value.SecretVersionID]
					if !ok {
						continue
					}
					value.SecretVersionID = &id
				}
				out = append(out, value)
			}
			return out
		}
		v.Spec.Variables = variables(v.Spec.Variables)
		for i := range v.Spec.Applications {
			a := &v.Spec.Applications[i]
			a.ID = ids[a.ID]
			a.Variables = variables(a.Variables)
			for j := range a.Mounts {
				a.Mounts[j].VolumeID = ids[a.Mounts[j].VolumeID]
			}
			for j := range a.DependsOn {
				a.DependsOn[j] = ids[a.DependsOn[j]]
			}
			if a.Postgres != nil {
				// The clone's database is new storage, so it starts on its major's current release, in
				// the owner model.
				fresh := domain.FreshPostgresImage(*a.Source.Image)
				a.Source.Image = &fresh
				a.Postgres.Roles = domain.PostgresOwner
				id, ok := secrets[a.Postgres.PasswordSecretVersionID]
				if !ok {
					pass, err := randtoken.New()
					if err != nil {
						return err
					}
					id, err = create("Database "+a.ID.String(), pass)
					if err != nil {
						return err
					}
				}
				a.Postgres.PasswordSecretVersionID = id
			}
		}
		if v.Spec.Placement != nil {
			for i := range v.Spec.Placement.Applications {
				placement := &v.Spec.Placement.Applications[i]
				placement.ApplicationID = ids[placement.ApplicationID]
			}
		}
		for i := range v.Spec.Volumes {
			v.Spec.Volumes[i].ID = ids[v.Spec.Volumes[i].ID]
		}
		for i := range v.Spec.Bindings {
			b := &v.Spec.Bindings[i]
			b.ID = ids[b.ID]
			b.SourceID = ids[b.SourceID]
			b.TargetID = ids[b.TargetID]
		}
		// A clone without copied secrets omits outputs backed by those variables,
		// and references to those outputs, just as it omits the variables themselves.
		for i := range v.Spec.Applications {
			a := &v.Spec.Applications[i]
			outputs := []domain.ServiceOutput{}
			for _, output := range a.Outputs {
				if output.Type == "variable" {
					if _, ok := domain.OutputVariable(v.Spec, *a, output.Variable); !ok {
						continue
					}
				}
				outputs = append(outputs, output)
			}
			a.Outputs = outputs
		}
		bindings := []domain.Binding{}
		for _, binding := range v.Spec.Bindings {
			for _, a := range v.Spec.Applications {
				if a.ID == binding.SourceID {
					if _, ok := domain.BindingOutput(a, binding.Output); ok {
						bindings = append(bindings, binding)
					}
				}
			}
		}
		v.Spec.Bindings = bindings
		for i := range v.Spec.Routes {
			r := &v.Spec.Routes[i]
			r.ID = ids[r.ID]
			r.TargetID = ids[r.TargetID]
			r.Domain = "clone-" + r.ID.String() + ".invalid"
		}
		if v.Spec.Compose != nil {
			v.Spec.Compose.Namespace = unit
		}
		raw, _ := stablejson.Marshal(v.Spec)
		_, canonical, err := domain.ParseSpec(raw)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(canonical)
		if _, err = store.InsertRevision(ctx, q, db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: rev, AuthorID: p.Account.ID, Specification: canonical, ContentHash: hash[:]}); err != nil {
			return err
		}
		if err = cloneCatalogStagings(ctx, q, p, ws, source, env, rev, ids, routes); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO deployment_units(workspace_id,id,environment_id) VALUES($1,$2,$3)", ws, unit, env); err != nil {
			return err
		}
		if _, err = q.PutLayout(ctx, db.PutLayoutParams{WorkspaceID: ws, EnvironmentID: env, Positions: []byte("{}")}); err != nil {
			return err
		}
		out, err = q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: env})
		return err
	})
	return out, err
}

func (s *Service) ExportVisual(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) (deployment.Export, error) {
	var out deployment.Export
	err := s.scope(ctx, p, ws, workspaceAccess{}, "environment.export", env, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		var id uuid.UUID
		err := tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND plan_id IS NOT NULL", ws, env).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			// A retired environment exports the configuration its retirement removed.
			id, err = db.New(tx).RetiredEnvironmentPlan(ctx, db.RetiredEnvironmentPlanParams{WorkspaceID: ws, ID: env})
		}
		if err != nil {
			return err
		}
		plan, err := readPlan(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		out, err = deployment.ExportConfiguration(plan)
		return err
	})
	return out, err
}

func (s *Service) ManagedVolumes(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]api.ManagedVolume, error) {
	out := []api.ManagedVolume{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		names := map[uuid.UUID]string{}
		used := map[uuid.UUID]bool{}
		if v.Applied != nil {
			var raw []byte
			if err = tx.QueryRow(ctx, "SELECT specification FROM specification_revisions WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, v.Applied).Scan(&raw); err != nil {
				return err
			}
			var previous domain.Specification
			if err = json.Unmarshal(raw, &previous); err != nil {
				return err
			}
			for _, vol := range previous.Volumes {
				names[vol.ID] = vol.Name
			}
		}
		for _, vol := range v.Spec.Volumes {
			names[vol.ID] = vol.Name
		}
		for _, app := range v.Spec.Applications {
			for _, mount := range app.Mounts {
				used[mount.VolumeID] = true
			}
		}
		ids := []uuid.UUID{}
		rows, err := tx.Query(ctx, "SELECT id,deleted FROM managed_volumes WHERE workspace_id=$1 AND environment_id=$2 AND unit_id=$3 ORDER BY id", ws, env, v.Unit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var volume api.ManagedVolume
			if err = rows.Scan(&volume.Id, &volume.Deleted); err != nil {
				return err
			}
			ids = append(ids, volume.Id)
			volume.Name = names[volume.Id]
			if volume.Name == "" {
				volume.Name = "Retained volume " + volume.Id.String()[:8]
			}
			volume.InUse = used[volume.Id]
			out = append(out, volume)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		// Shown, not decided on, so read without the lock a delete is decided under.
		deletions, err := db.New(tx).VolumeDeletions(ctx, db.VolumeDeletionsParams{WorkspaceID: ws, EnvironmentID: env, Ids: ids})
		if err != nil {
			return err
		}
		for _, d := range deletions {
			for i := range out {
				if out[i].Id == d.ID && !d.Deleted {
					out[i].Deleting = true
				}
			}
		}
		return nil
	})
	return out, err
}

// cloneCatalogStagings records each catalog staging of the source again for the clone, under the
// clone's identities and against its first revision, so the backups page recommends the same
// presets and planning checks the clone's copy of each route still staged in the source, as the
// clone's services start empty and its first account is new. A route the owner has since changed
// is the owner's choice in the clone too. A staging whose routes can't be read is recorded as
// unconfirmed, so planning in the clone asks for review. routes are the source's, before the clone
// gave them new identities.
func cloneCatalogStagings(ctx context.Context, q *db.Queries, p store.Principal, ws, source, env, rev uuid.UUID, ids map[uuid.UUID]uuid.UUID, routes []domain.Route) error {
	rows, err := q.ListCatalogStagingProvenance(ctx, db.ListCatalogStagingProvenanceParams{WorkspaceID: ws, EnvironmentID: source})
	if err != nil {
		return err
	}
	for _, row := range rows {
		services := row.Services
		var created map[string]uuid.UUID
		if json.Unmarshal(row.Services, &created) == nil {
			cloned := map[string]uuid.UUID{}
			for placeholder, id := range created {
				if next, ok := ids[id]; ok {
					cloned[placeholder] = next
				}
			}
			if services, err = stablejson.Marshal(cloned); err != nil {
				return err
			}
			created = cloned
		}
		recorded := []byte(unconfirmedStaging)
		staging := readStagingRoutes(row.StagedRoutes, row.Services, row.Specification)
		if !staging.unresolved && !staging.unconfirmed {
			kept := map[string][]string{}
			for _, r := range routes {
				if allow, ok := staging.staged[r.ID]; ok && slices.Equal(allow, r.AllowCIDRs) {
					kept[ids[r.ID].String()] = r.AllowCIDRs
				}
			}
			// Nothing of a staging the clone has left behind is worth recording.
			if len(kept) == 0 && len(created) == 0 {
				continue
			}
			if recorded, err = stablejson.Marshal(kept); err != nil {
				return err
			}
		}
		if err = q.InsertCatalogStaging(ctx, db.InsertCatalogStagingParams{WorkspaceID: ws, EnvironmentID: env, ID: uuid.New(), RecipeID: row.RecipeID, RecipeVersion: row.RecipeVersion, RevisionID: rev, Services: services, AuthorID: p.Account.ID, StagedRoutes: recorded}); err != nil {
			return err
		}
	}
	return nil
}
