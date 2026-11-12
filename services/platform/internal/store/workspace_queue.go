package store

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BeginWorkspaceWrite begins a transaction holding the workspace row lock. Callers
// wait in arrival order without holding a database connection: the lock admits one
// writer at a time, so waiting for it on a connection adds no throughput and lets one
// tenant's queue take the pool from every other tenant. Establish the caller first,
// because turns are kept for the life of the process, one per real workspace, and call
// it once with no transaction open: a turn cannot be taken twice, and PostgreSQL cannot
// see a deadlock through one. done rolls back unless the transaction committed, then
// releases the turn.
func (s *Store) BeginWorkspaceWrite(ctx context.Context, id uuid.UUID) (pgx.Tx, func(), error) {
	return s.beginWorkspaceWrite(ctx, id, pgx.ReadCommitted)
}

func (s *Store) beginWorkspaceWrite(ctx context.Context, id uuid.UUID, isolation pgx.TxIsoLevel) (pgx.Tx, func(), error) {
	value, _ := s.writeTurns.LoadOrStore(id, make(chan struct{}, 1))
	turn := value.(chan struct{})
	select {
	case turn <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
	if err != nil {
		<-turn
		return nil, nil, err
	}
	done := func() {
		tx.Rollback(ctx)
		<-turn
	}
	if _, err = db.New(tx).LockWorkspaceByID(ctx, id); err != nil {
		done()
		return nil, nil, err
	}
	return tx, done, nil
}

// WorkspaceWriteTransaction runs fn in a transaction holding the workspace row lock,
// under the terms of BeginWorkspaceWrite.
func (s *Store) WorkspaceWriteTransaction(ctx context.Context, id uuid.UUID, fn func(*db.Queries) error) error {
	return s.WorkspaceWriteTx(ctx, id, func(tx pgx.Tx) error { return fn(db.New(tx)) })
}

// WorkspaceWriteTx is WorkspaceWriteTransaction for a caller that needs the transaction itself.
func (s *Store) WorkspaceWriteTx(ctx context.Context, id uuid.UUID, fn func(pgx.Tx) error) error {
	tx, done, err := s.BeginWorkspaceWrite(ctx, id)
	if err != nil {
		return Normalize(err)
	}
	defer done()
	if err = fn(tx); err != nil {
		return Normalize(err)
	}
	return Normalize(tx.Commit(ctx))
}
