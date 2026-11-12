package execution

import (
	"context"
	"fmt"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) createPreviewEnvironment(ctx context.Context, tx pgx.Tx, copyKeys *passKeys, p previewPolicy, r *previewRecord) error {
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT specification FROM specification_revisions WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", p.Workspace, p.Template, p.Revision).Scan(&raw); err != nil {
		return err
	}
	template, _, err := domain.ParseSpec(raw)
	if err != nil {
		return err
	}
	env, rev, unit := uuid.New(), uuid.New(), uuid.New()
	q := db.New(tx)
	if _, err = q.InsertEnvironment(ctx, db.InsertEnvironmentParams{WorkspaceID: p.Workspace, ProjectID: p.Project, ID: env, Name: fmt.Sprintf("Preview %s #%d", r.View.Id, r.View.Generation+1), DraftRevisionID: rev, ConfigurationOwner: "dashboard", Purpose: "preview"}); err != nil {
		return err
	}
	ids := map[uuid.UUID]uuid.UUID{}
	for _, a := range template.Applications {
		ids[a.ID] = uuid.New()
	}
	for _, v := range template.Volumes {
		ids[v.ID] = uuid.New()
	}
	for _, b := range template.Bindings {
		ids[b.ID] = uuid.New()
	}
	for _, route := range template.Routes {
		ids[route.ID] = uuid.New()
	}
	passwords := map[uuid.UUID]bool{}
	for _, a := range template.Applications {
		if a.Postgres != nil {
			passwords[a.Postgres.PasswordSecretVersionID] = true
		}
	}
	secrets := map[uuid.UUID]uuid.UUID{}
	references := domain.SecretReferences(template)
	var keys *secretbroker.Session
	if len(references) > 0 {
		if keys, err = copyKeys.get(); err != nil {
			return err
		}
	}
	for _, old := range references {
		var value string
		if passwords[old] {
			value, err = randtoken.New()
		} else {
			var v db.SecretVersion
			v, err = q.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: p.Workspace, EnvironmentID: p.Template, ID: old})
			if err == nil {
				var plain []byte
				plain, err = keys.Open(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: old}, v.EncryptedValue)
				value = string(plain)
				clear(plain)
			}
		}
		if err != nil {
			return err
		}
		sid, id := uuid.New(), uuid.New()
		if err = q.InsertSecret(ctx, db.InsertSecretParams{WorkspaceID: p.Workspace, EnvironmentID: env, ID: sid, Name: "Preview " + old.String()}); err != nil {
			return err
		}
		cipher, err := keys.Seal(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: id}, []byte(value))
		if err != nil {
			return err
		}
		if _, err = q.InsertSecretVersion(ctx, db.InsertSecretVersionParams{WorkspaceID: p.Workspace, SecretID: sid, ID: id, EncryptedValue: cipher, AuthorID: p.Actor}); err != nil {
			return err
		}
		copyKeys.use()
		secrets[old] = id
	}
	spec, err := domain.RemapPreview(template, ids, secrets, func(app uuid.UUID) string {
		if !p.Config.Domains {
			return ""
		}
		return domain.ApplicationHostname(s.Store.ApplicationDomain, p.Workspace, env, app, p.Server)
	})
	if err != nil {
		return err
	}
	raw, err = stablejson.Marshal(spec)
	if err != nil {
		return err
	}
	if _, err = store.InsertRevision(ctx, q, db.InsertRevisionParams{WorkspaceID: p.Workspace, EnvironmentID: env, ID: rev, AuthorID: p.Actor, Specification: raw, ContentHash: hash(string(raw))}); err != nil {
		return err
	}
	if err = q.InsertDeploymentUnit(ctx, db.InsertDeploymentUnitParams{WorkspaceID: p.Workspace, EnvironmentID: env, ID: unit}); err != nil {
		return err
	}
	if _, err = q.PutLayout(ctx, db.PutLayoutParams{WorkspaceID: p.Workspace, EnvironmentID: env, Positions: []byte("{}")}); err != nil {
		return err
	}
	cfg, _ := stablejson.Marshal(p.Config)
	if _, err = tx.Exec(ctx, "UPDATE deployment_units SET server_id=$2 WHERE workspace_id=$1 AND id=$3", p.Workspace, p.Server, unit); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE preview_requests SET environment_id=$2,application_id=$3,configuration=$4,policy_generation=$5,generation=generation+1,build_id=NULL,plan_id=NULL,operation_id=NULL,built_head='' WHERE id=$1", r.View.Id, env, ids[p.App], cfg, p.Generation); err != nil {
		return err
	}
	r.View.EnvironmentId = &env
	app := ids[p.App]
	r.App = &app
	r.Config = &p.Config
	r.PolicyGeneration = p.Generation
	r.View.Generation++
	return nil
}
