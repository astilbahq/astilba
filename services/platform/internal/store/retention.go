package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/diagnostic"
)

// PruneHistory removes bounded diagnostic payloads. Configuration, ownership,
// artifact identities and audit history remain available for recovery.
//
// Each class of cleanup commits on its own, so one that fails on a row from any
// workspace does not undo or stop the others. It is retried on the next run.
func (s *Store) PruneHistory(ctx context.Context) error {
	cutoff := time.Now().Add(-s.LogRetention)
	statement := func(sql string, args ...any) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := s.Pool.Exec(ctx, sql, args...)
			return err
		}
	}
	cleanups := []struct {
		name string
		run  func(context.Context) error
	}{
		{"operation_diagnostics", statement(`UPDATE execution_operations SET steps='[]',note='' WHERE id IN (SELECT id FROM execution_operations WHERE created_at<$1 AND state IN ('succeeded','failed','cancelled') AND (steps<>'[]'::jsonb OR note<>'') ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED)`, cutoff)},
		{"build_logs", statement(`UPDATE pipeline_runs SET result=result-'logs' WHERE id IN (SELECT p.id FROM pipeline_runs p JOIN execution_operations o ON o.id=p.id WHERE p.created_at<$1 AND o.state IN ('succeeded','failed','cancelled') AND jsonb_typeof(p.result)='object' AND p.result ? 'logs' ORDER BY p.created_at LIMIT 100 FOR UPDATE OF p SKIP LOCKED)`, cutoff)},
		// Imported receipt JSON can contain older, unchecked shapes. Only recognized
		// resource objects participate; malformed branches must not block other cleanup.
		{"cross_host_receipts", statement(`UPDATE cross_host_operations h SET receipts=(
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
)`, cutoff)},
		{"billing_events", statement(`DELETE FROM hosted_billing_events WHERE id IN (SELECT id FROM hosted_billing_events WHERE received_at<now()-interval '90 days' LIMIT 1000 FOR UPDATE SKIP LOCKED)`)},
		{"invitations", statement(`DELETE FROM hosted_invitations WHERE id IN (SELECT id FROM hosted_invitations WHERE expires_at<now()-interval '1 day' LIMIT 1000 FOR UPDATE SKIP LOCKED)`)},
		{"email_verifications", statement(`DELETE FROM email_verifications WHERE account_id IN (SELECT account_id FROM email_verifications WHERE expires_at<now() LIMIT 1000 FOR UPDATE SKIP LOCKED)`)},
		{"password_resets", statement(`DELETE FROM password_resets WHERE account_id IN (SELECT account_id FROM password_resets WHERE expires_at<now() LIMIT 1000 FOR UPDATE SKIP LOCKED)`)},
		{"server_metrics", statement(`DELETE FROM server_metrics WHERE (server_id,received_at) IN (SELECT server_id,received_at FROM server_metrics WHERE received_at<=now()-interval '24 hours' ORDER BY received_at LIMIT 1000 FOR UPDATE SKIP LOCKED)`)},
		{"git_branch_pushes", s.Queries.PruneGitBranchPushes},
		{"agent_nonces", s.Queries.PruneAgentNonces},
		{"agent_identity_events", s.Queries.PruneAgentIdentityEvents},
		{"agent_keys", s.Queries.PruneAgentKeys},
		{"github_authorizations", statement(`DELETE FROM github_app_authorizations WHERE id IN (SELECT id FROM github_app_authorizations WHERE expires_at<now() ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED)`)},
	}
	var failed []error
	for _, cleanup := range cleanups {
		step, cancel := context.WithTimeout(ctx, retentionStepLimit)
		err := cleanup.run(step)
		cancel()
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			slog.Error("retention cleanup failed", "class", cleanup.name, diagnostic.Failure(err))
			failed = append(failed, fmt.Errorf("%s: %w", cleanup.name, err))
		}
	}
	return errors.Join(failed...)
}

// Steps are batched, so a limit only cuts short one stuck on a lock or a slow scan.
const retentionStepLimit = 10 * time.Second
