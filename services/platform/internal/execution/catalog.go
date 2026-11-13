package execution

import (
	"context"
	"encoding/json/v2"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/catalog"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// StageCatalogEntry adds a catalog entry to the environment's draft under fresh identities and
// saves the draft, as a draft save would. The entry's secrets are generated into the secret
// store in the same transaction, so their values never leave management and nothing is stored
// for a staging that fails. Like a clone's generated passwords, they need write access but no
// recent sign-in: nobody sees them.
func (s *Service) StageCatalogEntry(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, in api.CatalogStageRequest) (db.SpecificationRevision, error) {
	var out db.SpecificationRevision
	recipe, ok := catalog.Find(in.RecipeId)
	if !ok {
		return out, domain.Invalid("recipeId")
	}
	stage := catalog.Stage{Name: in.Name, Existing: map[uuid.UUID]uuid.UUID{}}
	if in.ExistingSecrets != nil {
		for _, e := range *in.ExistingSecrets {
			if _, seen := stage.Existing[e.SecretId]; seen {
				return out, domain.Invalid("existingSecrets")
			}
			stage.Existing[e.SecretId] = e.SecretVersionId
		}
	}
	var server *uuid.UUID
	if r := in.Route; r != nil {
		domainName := ""
		if r.Domain != nil {
			domainName = *r.Domain
		}
		if (domainName == "") == (r.GeneratedServerId == nil) {
			return out, domain.Invalid("route.domain")
		}
		server = r.GeneratedServerId
		stage.Route = &catalog.StageRoute{Domain: domainName, TLS: string(r.Tls), AllowCIDRs: r.AllowCidrs}
	}
	keys, err := s.Store.BeginKeys(ctx, p, store.WorkspaceAccess{Write: true}, false, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Seal, Kinds: []secretbroker.Kind{secretbroker.SecretVersion}})
	if err != nil {
		return out, err
	}
	used := false
	defer func() { keys.Close(ctx, secretbroker.UsedIf(used)) }()
	staging := uuid.New()
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true}, "catalog.stage", staging, func(tx pgx.Tx) error {
		q := db.New(tx)
		row, err := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: env})
		if err != nil {
			return err
		}
		if row.DraftRevisionID != in.BaseRevisionId {
			return domain.Conflict
		}
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if row.ConfigurationOwner != "dashboard" || v.Spec.Compose != nil {
			return domain.Invalid("configurationOwner")
		}
		for _, version := range stage.Existing {
			if _, err := q.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: env, ID: version}); err != nil {
				return store.IfMissing(err, domain.Invalid("existingSecrets"))
			}
		}
		staged, err := recipe.Stage(stage, func(app uuid.UUID) (string, error) {
			// The same rules as a suggested domain: the server must be the environment's, if it
			// has one, and its application-domain addresses must be in use.
			if v.Server != nil && *v.Server != *server {
				return "", domain.Invalid("route.generatedServerId")
			}
			config, err := s.domainConfiguration(ctx, tx, ws, *server)
			if err != nil {
				return "", err
			}
			if config.State == "disabled" || config.State == "unconfigured" || config.State == "review_required" {
				return "", domain.Invalid("route.generatedServerId")
			}
			return domain.ApplicationHostname(s.Store.ApplicationDomain, ws, env, app, *server), nil
		})
		if err != nil {
			return err
		}
		spec := v.Spec
		for _, a := range staged.Specification.Applications {
			for _, existing := range spec.Applications {
				if strings.EqualFold(existing.Name, a.Name) {
					return &domain.Error{Code: "invalid_input", Message: "The environment already has a service named " + a.Name + ". Stage under another name.", Fields: []string{"name"}}
				}
			}
		}
		if len(staged.Generated) > 0 {
			held, err := q.ListSecrets(ctx, db.ListSecretsParams{WorkspaceID: ws, EnvironmentID: env})
			if err != nil {
				return err
			}
			for _, g := range staged.Generated {
				for _, h := range held {
					if h.Name == g.Name {
						return &domain.Error{Code: "invalid_input", Message: "The environment already has a secret named " + g.Name + ". Stage under another name.", Fields: []string{"name"}}
					}
				}
			}
		}
		spec.Applications = append(spec.Applications, staged.Specification.Applications...)
		spec.Volumes = append(spec.Volumes, staged.Specification.Volumes...)
		spec.Bindings = append(spec.Bindings, staged.Specification.Bindings...)
		spec.Routes = append(spec.Routes, staged.Specification.Routes...)
		raw, err := stablejson.Marshal(spec)
		if err != nil {
			return err
		}
		spec, canonical, err := domain.ParseSpec(raw)
		if err != nil {
			return err
		}
		for _, g := range staged.Generated {
			value, err := g.Value()
			if err != nil {
				return err
			}
			used = true
			if err = store.InsertSecretValue(ctx, q, keys, p, ws, env, g.VersionID, g.Name, value); err != nil {
				return err
			}
		}
		if out, err = s.Store.SaveDraft(ctx, tx, q, p, row, spec, canonical); err != nil {
			return err
		}
		services := map[string]uuid.UUID{}
		for _, a := range recipe.Specification.Applications {
			services[a.ID.String()] = staged.Identities[a.ID]
		}
		encoded, err := stablejson.Marshal(services)
		if err != nil {
			return err
		}
		return q.InsertCatalogStaging(ctx, db.InsertCatalogStagingParams{WorkspaceID: ws, EnvironmentID: env, ID: staging, RecipeID: recipe.ID, RecipeVersion: recipe.Version, RevisionID: out.ID, Services: encoded, AuthorID: p.Account.ID})
	})
	return out, err
}

// CatalogStagings lists what was staged into the environment, with each entry's backup presets
// for the services it created. The caller compares them with the environment's specification
// and backup policies.
func (s *Service) CatalogStagings(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]api.CatalogStaging, error) {
	out := []api.CatalogStaging{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: env}); err != nil {
			return err
		}
		rows, err := q.ListCatalogStagings(ctx, db.ListCatalogStagingsParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		for _, row := range rows {
			var services map[uuid.UUID]uuid.UUID
			if err := json.Unmarshal(row.Services, &services); err != nil {
				return err
			}
			v := api.CatalogStaging{Id: row.ID, RecipeId: row.RecipeID, RecipeVersion: row.RecipeVersion, RevisionId: row.RevisionID, CreatedAt: row.CreatedAt, Backups: []api.CatalogBackupPreset{}}
			if recipe, ok := catalog.Find(row.RecipeID); ok {
				v.RecipeName = recipe.Name
				for _, b := range recipe.Backups {
					if id, ok := services[b.ServiceID]; ok {
						v.Backups = append(v.Backups, api.CatalogBackupPreset{ServiceId: id, Method: api.CatalogBackupPresetMethod(b.Method), Hour: b.Hour, Minute: b.Minute, Retain: b.Retain})
					}
				}
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}
