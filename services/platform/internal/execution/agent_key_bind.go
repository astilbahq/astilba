package execution

import (
	"bytes"
	"context"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Bind registers a key for a server that has so far held a bearer credential. The current bearer
// itself authenticates it, so a bound server's retired bearer can never bind again.
func (s *Service) Bind(ctx context.Context, server uuid.UUID, bearer string, in api.AgentKeyBind, source string) error {
	if len(bearer) != 43 {
		return domain.Unauthenticated
	}
	public, der, err := agentidentity.ParsePublicKey(in.PublicKey)
	if err != nil {
		return domain.Invalid("publicKey")
	}
	keyHash := agentidentity.KeyHash(der)
	if !agentidentity.Verify(public, agentidentity.BindMessage(server, in.MachineId, keyHash), in.Proof) {
		return domain.Unauthenticated
	}
	presented := hash(bearer)
	// Only this server's bearer, or the one its bind retired, takes the row lock.
	if known, err := s.Store.Queries.BearerKnown(ctx, db.BearerKnownParams{BearerHash: presented, ServerID: server}); errors.Is(err, pgx.ErrNoRows) || (err == nil && !known) {
		return domain.Unauthenticated
	} else if err != nil {
		return err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	row, err := q.LockServerIdentity(ctx, server)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Unauthenticated
	}
	if err != nil {
		return err
	}
	if row.Revoked || row.AwaitingTransfer || row.CredentialHash == nil || row.MachineID == nil || *row.MachineID != in.MachineId {
		return domain.Unauthenticated
	}
	if row.RetiredBearerHash != nil && bytes.Equal(presented, row.RetiredBearerHash) {
		if bytes.Equal(row.CredentialHash, keyHash) {
			// A retry of the bind that retired this bearer.
			return nil
		}
		// The honest agent's bind key is fixed before it sends: another key is another holder.
		if err = s.conflict(ctx, q, row.WorkspaceID, server, row.EnrollmentEpoch, signalBindKey, keyHash, source); err != nil {
			return err
		}
		return commitRefusal(ctx, tx, keyNotCurrent)
	}
	if !bytes.Equal(row.CredentialHash, presented) {
		return domain.Unauthenticated
	}
	if err = q.BindServerKey(ctx, db.BindServerKeyParams{KeyHash: keyHash, BearerHash: presented, WorkspaceID: row.WorkspaceID, ServerID: server}); err != nil {
		return err
	}
	epoch := row.EnrollmentEpoch + 1
	if err = q.InsertAgentKey(ctx, db.InsertAgentKeyParams{WorkspaceID: row.WorkspaceID, ServerID: server, KeyHash: keyHash, PublicKey: der, Epoch: epoch}); err != nil {
		return err
	}
	if err = identityEvent(ctx, q, row.WorkspaceID, server, epoch, "bound", "", keyHash, source); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RetiredBearer checks a refused bearer against the one its server's bind retired. Inside the
// window where only another holder can present it, that proves a second holder.
func (s *Service) RetiredBearer(ctx context.Context, server uuid.UUID, bearer, source string) error {
	if len(bearer) != 43 {
		return nil
	}
	presented := hash(bearer)
	// Anything else that was refused takes no lock and records nothing.
	known, err := s.Store.Queries.RetiredBearerKnown(ctx, db.RetiredBearerKnownParams{BearerHash: presented, ServerID: server})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !known) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	row, err := q.LockRetiredBearer(ctx, db.LockRetiredBearerParams{BearerHash: presented, ServerID: server})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !row.Retired {
		return nil
	}
	if row.InWindow {
		err = s.conflict(ctx, q, row.WorkspaceID, server, row.EnrollmentEpoch, signalRetiredBearer, nil, source)
	} else {
		err = identityEvent(ctx, q, row.WorkspaceID, server, row.EnrollmentEpoch, "refused", signalRetiredBearer, nil, source)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
