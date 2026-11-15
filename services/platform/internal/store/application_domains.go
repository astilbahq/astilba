package store

import (
	"context"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Configuration, revocation, recovery and DNS jobs share this server-scoped lock. A DNS job
// takes it only to check an approval and record the name it will publish, in one short
// transaction; it asks the provider holding the server's lease, with nothing open.
func LockServerDNS(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,32))", id.String())
	return e
}
func (s *Store) ValidateApplicationRoutes(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, spec domain.Specification, server *uuid.UUID) error {
	ids, e := domain.GeneratedRouteServers(s.ApplicationDomain, ws, env, spec, server)
	if e != nil {
		return e
	}
	for _, id := range ids {
		var valid bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM servers WHERE workspace_id=$1 AND id=$2 AND role IN ('deployment','combined') AND NOT revoked)", ws, id).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return domain.Invalid("routes.generatedDomain")
		}
	}
	return nil
}
