package store

import (
	"context"
	"time"
)

// PruneHistory removes bounded diagnostic payloads. Configuration, ownership,
// artifact identities and audit history remain available for recovery.
func (s *Store) PruneHistory(ctx context.Context) error {
	cutoff := time.Now().Add(-s.LogRetention)
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE execution_operations SET steps='[]',note='' WHERE id IN (SELECT id FROM execution_operations WHERE created_at<$1 AND state IN ('succeeded','failed','cancelled') AND (steps<>'[]'::jsonb OR note<>'') ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED)`, cutoff)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE pipeline_runs SET result=result-'logs' WHERE id IN (SELECT p.id FROM pipeline_runs p JOIN execution_operations o ON o.id=p.id WHERE p.created_at<$1 AND o.state IN ('succeeded','failed','cancelled') AND p.result ? 'logs' ORDER BY p.created_at LIMIT 100 FOR UPDATE OF p SKIP LOCKED)`, cutoff)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM hosted_billing_events WHERE received_at<now()-interval '90 days'`)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM hosted_invitations WHERE expires_at<now()-interval '1 day'`)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM email_verifications WHERE expires_at<now()`)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM password_resets WHERE expires_at<now()`)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM server_metrics WHERE (server_id,received_at) IN (SELECT server_id,received_at FROM server_metrics WHERE received_at<=now()-interval '24 hours' ORDER BY received_at LIMIT 1000 FOR UPDATE SKIP LOCKED)`)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
