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
	// Imported receipt JSON can contain older, unchecked shapes. Only recognized
	// resource objects participate; malformed branches must not block other cleanup.
	_, e = tx.Exec(ctx, `UPDATE cross_host_operations h SET receipts=(
 SELECT jsonb_object_agg(receipt.key, CASE WHEN jsonb_typeof(receipt.value->'observation'->'resources')='array' THEN
  jsonb_set(receipt.value,'{observation,resources}',COALESCE((
   SELECT jsonb_agg(CASE WHEN jsonb_typeof(resource.value)='object' THEN resource.value-'logs' ELSE resource.value END ORDER BY resource.position)
   FROM jsonb_array_elements(receipt.value->'observation'->'resources') WITH ORDINALITY resource(value,position)
  ),'[]'::jsonb)) ELSE receipt.value END)
 FROM jsonb_each(h.receipts) receipt
) WHERE h.id IN (
 SELECT h.id FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id
 WHERE o.created_at<$1 AND o.state IN ('succeeded','failed','cancelled')
 AND EXISTS (
  SELECT 1 FROM jsonb_each(CASE WHEN jsonb_typeof(h.receipts)='object' THEN h.receipts ELSE '{}'::jsonb END) receipt
  CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(receipt.value->'observation'->'resources')='array' THEN receipt.value->'observation'->'resources' ELSE '[]'::jsonb END) resource
  WHERE jsonb_typeof(resource.value)='object' AND resource.value ? 'logs'
 )
 ORDER BY o.created_at,h.id LIMIT 100 FOR UPDATE OF h SKIP LOCKED
)`, cutoff)
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
	_, e = tx.Exec(ctx, `DELETE FROM github_app_authorizations WHERE id IN (SELECT id FROM github_app_authorizations WHERE expires_at<now() ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED)`)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
