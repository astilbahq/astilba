package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
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
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true, Recent: in.CopySecrets}, "environment.clone", source, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, source)
		if err != nil {
			return err
		}
		if v.Purpose != "standard" {
			return domain.Invalid("preview.cloneUnsupported")
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
			sid, id := uuid.New(), uuid.New()
			if err := q.InsertSecret(ctx, db.InsertSecretParams{WorkspaceID: ws, EnvironmentID: env, ID: sid, Name: label}); err != nil {
				return uuid.Nil, err
			}
			encrypted, err := s.Store.Keys.Seal([]byte(value), store.SecretIdentity(ws, id))
			if err != nil {
				return uuid.Nil, err
			}
			_, err = q.InsertSecretVersion(ctx, db.InsertSecretVersionParams{WorkspaceID: ws, SecretID: sid, ID: id, EncryptedValue: encrypted, AuthorID: p.Account.ID})
			return id, err
		}
		if in.CopySecrets {
			for _, old := range domain.SecretReferences(v.Spec) {
				secret, err := q.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: source, ID: old})
				if err != nil {
					return err
				}
				plain, err := s.Store.Keys.Open(secret.EncryptedValue, store.SecretIdentity(ws, old))
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
				id, ok := secrets[a.Postgres.PasswordSecretVersionID]
				if !ok {
					pass, err := cryptobox.Token()
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
		raw, _ := json.Marshal(v.Spec)
		_, canonical, err := domain.ParseSpec(raw)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(canonical)
		if _, err = q.InsertRevision(ctx, db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: rev, AuthorID: p.Account.ID, Specification: canonical, ContentHash: hash[:]}); err != nil {
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
		if err := tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND plan_id IS NOT NULL", ws, env).Scan(&id); err != nil {
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
			volume.Name = names[volume.Id]
			if volume.Name == "" {
				volume.Name = "Retained volume " + volume.Id.String()[:8]
			}
			volume.InUse = used[volume.Id]
			out = append(out, volume)
		}
		return rows.Err()
	})
	return out, err
}
