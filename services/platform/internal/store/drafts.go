package store

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SaveDraft makes spec, which ParseSpec read as canonical, the environment's draft after the
// checks every new draft passes. The caller has checked that the draft is still the revision
// the save is based on. Saving the draft unchanged adds no revision: the kept revision shows
// who saved it and when, so only a principal that may read the environment gets it back, and
// any other gets a new revision.
func (s *Store) SaveDraft(ctx context.Context, tx pgx.Tx, q *db.Queries, p Principal, env db.GetEnvironmentRow, spec domain.Specification, canonical []byte) (db.SpecificationRevision, error) {
	ws, id := env.WorkspaceID, env.ID
	if e := s.ValidateApplicationRoutes(ctx, tx, ws, id, spec, nil); e != nil {
		return db.SpecificationRevision{}, e
	}
	var applied domain.Specification
	if env.AppliedRevisionID != nil {
		v, e := q.GetRevision(ctx, db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: *env.AppliedRevisionID})
		if e != nil {
			return db.SpecificationRevision{}, e
		}
		if e = json.Unmarshal(v.Specification, &applied); e != nil {
			return db.SpecificationRevision{}, e
		}
	}
	if e := domain.ValidateNewDraft(spec, applied); e != nil {
		return db.SpecificationRevision{}, e
	}
	for _, secretID := range domain.SecretReferences(spec) {
		if _, e := q.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: id, ID: secretID}); e != nil {
			return db.SpecificationRevision{}, IfMissing(e, domain.Invalid("specification.secretVersionId"))
		}
	}
	if p.MayReadEnvironment() {
		same, e := q.GetRevisionHolding(ctx, db.GetRevisionHoldingParams{WorkspaceID: ws, EnvironmentID: id, ID: env.DraftRevisionID, Specification: canonical})
		if e == nil || !errors.Is(e, pgx.ErrNoRows) {
			return same, e
		}
	}
	newID := uuid.New()
	h := sha256.Sum256(canonical)
	rev, e := InsertRevision(ctx, q, db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: newID, AuthorID: p.Account.ID, Specification: canonical, ContentHash: h[:]})
	if e != nil {
		return rev, e
	}
	if _, e := q.AdvanceDraft(ctx, db.AdvanceDraftParams{WorkspaceID: ws, ID: id, DraftRevisionID: env.DraftRevisionID, DraftRevisionID_2: newID}); e != nil {
		return rev, e
	}
	return rev, Audit(ctx, q, p, ws, "draft.save", newID)
}

// InsertSecretValue stores value, sealed by keys, as version, the first version of a new secret
// in the environment. Clones and staged catalog entries store what they generate through it.
func InsertSecretValue(ctx context.Context, q *db.Queries, keys *secretbroker.Session, p Principal, ws, env, version uuid.UUID, name, value string) error {
	secret := uuid.New()
	if err := q.InsertSecret(ctx, db.InsertSecretParams{WorkspaceID: ws, EnvironmentID: env, ID: secret, Name: name}); err != nil {
		return err
	}
	encrypted, err := keys.Seal(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: version}, []byte(value))
	if err != nil {
		return err
	}
	_, err = q.InsertSecretVersion(ctx, db.InsertSecretVersionParams{WorkspaceID: ws, SecretID: secret, ID: version, EncryptedValue: encrypted, AuthorID: p.Account.ID})
	return err
}
