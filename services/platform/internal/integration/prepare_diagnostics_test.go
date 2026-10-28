package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
)

// One statement gives a consistent failure-time view. The projections omit
// payloads, notes, credentials, addresses and raw configuration; lists total at
// most 190 rows, and the caller bounds the query to two seconds.
const upgradePrepareDiagnosticSQL = `
WITH target AS (
 SELECT plan,status FROM supported_upgrades WHERE workspace_id=$1 AND environment_id=$2 AND id=$3
), operations AS (
 SELECT o.id,o.server_id,o.kind,o.state,o.parent_id,o.cancel_requested,o.created_at,j.state AS job_state,
  CASE WHEN o.command->'network'->>'action' IN ('identify','configure','restrict') THEN o.command->'network'->>'action' END AS network_action
 FROM execution_operations o LEFT JOIN river_job j ON j.id=o.job_id
 WHERE o.workspace_id=$1 AND o.server_id=ANY($5::uuid[])
  AND (o.state NOT IN ('succeeded','failed','cancelled') OR (o.kind='network' AND o.created_at>=$7::timestamptz-interval '15 seconds'))
 ORDER BY (o.state NOT IN ('succeeded','failed','cancelled')) DESC,o.created_at DESC,o.id LIMIT 150
), reservations AS (
 SELECT r.server_id,r.parent_id,o.state AS parent_state
 FROM cross_host_reservations r LEFT JOIN execution_operations o ON o.id=r.parent_id
 WHERE r.workspace_id=$1 AND r.server_id=ANY($5::uuid[]) ORDER BY r.server_id LIMIT 10
), upgrades AS (
 SELECT id,server_id,
  CASE WHEN status->>'phase' IN ('planned','rejected','running','ready','attention','complete','returned') THEN status->>'phase' ELSE 'invalid' END AS phase
 FROM supported_upgrades WHERE workspace_id=$1 AND server_id=ANY($5::uuid[])
  AND (id=$3 OR status->>'phase' NOT IN ('planned','rejected','complete','returned')) ORDER BY id LIMIT 10
), units AS (
 SELECT expected.unit_id,expected.server_id,r.unit_id IS NOT NULL AS runtime_present,
  r.inventory->'plan'->>'hash'=expected.plan_hash AS plan_hash_matches,
  length(r.inventory->'observation'->>'stateHash') AS state_hash_length
 FROM jsonb_to_recordset($6::jsonb) AS expected(unit_id uuid,server_id uuid,plan_hash text)
 LEFT JOIN cross_host_runtime r ON r.workspace_id=$1 AND r.environment_id=$2 AND r.unit_id=expected.unit_id
 ORDER BY expected.unit_id LIMIT 10
), network AS (
 SELECT s.id AS server_id,s.revoked,s.awaiting_transfer,s.preview_only,s.controller_generation,
  extract(epoch FROM statement_timestamp()-s.last_seen)::bigint AS heartbeat_age_seconds,
  n.generation AS network_generation,m.enabled,m.generation,m.acknowledged_generation,m.approved_controller,
  CASE WHEN jsonb_typeof(m.installed_manifest->'generation')='number' THEN m.installed_manifest->'generation' END AS installed_generation
 FROM servers s LEFT JOIN network_members m ON m.workspace_id=s.workspace_id AND m.server_id=s.id
 LEFT JOIN workspace_networks n ON n.workspace_id=m.workspace_id AND n.id=m.network_id
 WHERE s.workspace_id=$1 AND s.id=ANY($5::uuid[]) ORDER BY s.id LIMIT 10
)
SELECT jsonb_build_object(
 'capturedAt',statement_timestamp(),'requestStartedAt',$7::timestamptz,
 'upgrade',(SELECT jsonb_build_object(
  'present',true,'planMatchesCreated',u.plan=$8::jsonb,'statusMatchesCreated',u.status=$10::jsonb,
  'phase',CASE WHEN u.status->>'phase' IN ('planned','rejected','running','ready','attention','complete','returned') THEN u.status->>'phase' ELSE 'invalid' END,
  'expired',CASE WHEN pg_input_is_valid(u.plan->>'expiresAt','timestamp with time zone') THEN (u.plan->>'expiresAt')::timestamptz<statement_timestamp() END,
  'appliedRevisionMatchesSource',e.applied_revision_id::text=u.plan#>>'{source,revisionId}',
  'runtimePlanMatchesSource',r.plan_id::text=COALESCE(u.plan#>>'{source,local,groupId}',u.plan#>>'{source,id}'),
  'policyPresent',p.id IS NOT NULL,'policyHashMatchesSource',p.configuration#>>'{plan,hash}'=u.plan#>>'{source,hash}',
  'policyGenerationMatchesCreated',p.configuration->'generation'=$9::jsonb
 ) FROM target u LEFT JOIN environments e ON e.workspace_id=$1 AND e.id=$2
 LEFT JOIN visual_runtime r ON r.workspace_id=$1 AND r.environment_id=$2
 LEFT JOIN backup_policies p ON p.workspace_id=$1 AND p.environment_id=$2 AND p.id::text=u.plan->>'policyId'),
 'requestKey',(SELECT jsonb_build_object('id',id,'kind',kind,'state',state,
  'upgradeMatches',COALESCE(command#>>'{upgrade,plan,id}',command#>>'{crosshost,maintenance,effect,upgrade,plan,id}')=$3::text,
  'actionMatches',COALESCE(command#>>'{upgrade,action}',command#>>'{crosshost,maintenance,effect,upgrade,action}')='prepare')
  FROM execution_operations WHERE workspace_id=$1 AND request_key=$4),
 'operations',COALESCE((SELECT jsonb_agg(to_jsonb(o)) FROM operations o),'[]'::jsonb),
 'reservations',COALESCE((SELECT jsonb_agg(to_jsonb(r)) FROM reservations r),'[]'::jsonb),
 'upgrades',COALESCE((SELECT jsonb_agg(to_jsonb(u)) FROM upgrades u),'[]'::jsonb),
 'units',COALESCE((SELECT jsonb_agg(to_jsonb(u)) FROM units u),'[]'::jsonb),
 'network',COALESCE((SELECT jsonb_agg(to_jsonb(n)) FROM network n),'[]'::jsonb)
)`

func logUpgradePrepareFailure(t *testing.T, f *fixture, ws, env uuid.UUID, view upgrade.View, policyGeneration int64, key uuid.UUID, started time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	plan := view.Plan
	type unit struct {
		ID       uuid.UUID `json:"unit_id"`
		ServerID uuid.UUID `json:"server_id"`
		Hash     string    `json:"plan_hash"`
	}
	servers := []uuid.UUID{plan.Source.ServerID}
	units := []unit{}
	if plan.Group != nil {
		for _, u := range plan.Group.ActiveUnits() {
			servers = append(servers, u.Plan.ServerID)
			units = append(units, unit{u.Plan.UnitID, u.Plan.ServerID, u.Plan.Hash})
		}
	}
	unitData, _ := json.Marshal(units)
	planData, _ := json.Marshal(plan)
	statusData, _ := json.Marshal(view.Status)
	generationData, _ := json.Marshal(policyGeneration)
	var diagnostic []byte
	if err := f.st.Pool.QueryRow(ctx, upgradePrepareDiagnosticSQL, ws, env, plan.ID, key, servers, unitData, started, planData, generationData, statusData).Scan(&diagnostic); err != nil {
		t.Logf("upgrade prepare diagnostic unavailable (%T)", err)
		return
	}
	t.Logf("upgrade prepare diagnostic: %s", diagnostic)
}
