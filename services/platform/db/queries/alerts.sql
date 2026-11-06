-- name: AllAlertWebhooks :many
SELECT workspace_id,encrypted_webhook FROM alert_settings WHERE encrypted_webhook IS NOT NULL FOR UPDATE;
-- name: RewrapAlertWebhook :exec
UPDATE alert_settings SET encrypted_webhook=$2 WHERE workspace_id=$1;

-- name: AlertSettings :one
SELECT email_enabled,(encrypted_webhook IS NOT NULL)::boolean AS webhook_enabled,revision
FROM alert_settings WHERE workspace_id = @workspace_id;

-- name: AlertWebhookCipher :one
SELECT encrypted_webhook FROM alert_settings WHERE workspace_id = @workspace_id;

-- name: UpsertAlertSettings :exec
INSERT INTO alert_settings(workspace_id,email_enabled,encrypted_webhook)
VALUES(@workspace_id,@email_enabled,@encrypted_webhook)
ON CONFLICT(workspace_id) DO UPDATE SET email_enabled=EXCLUDED.email_enabled,
    encrypted_webhook=EXCLUDED.encrypted_webhook,revision=alert_settings.revision+1;

-- name: CancelWorkspaceAlertDeliveries :exec
UPDATE alert_deliveries SET state='cancelled'
WHERE workspace_id = @workspace_id AND state IN ('pending','leased');

-- name: AlertOpenedAt :one
SELECT opened_at FROM operational_alerts WHERE workspace_id = @workspace_id AND id = @id;

-- name: ListAlerts :many
SELECT a.id,a.kind,a.resource_id,a.server_id,a.environment_id,a.state,a.reason,a.opened_at,a.resolved_at,a.acknowledged_at,
    COALESCE((SELECT jsonb_agg(d.value) FROM (
        SELECT jsonb_build_object('channel',channel,'phase',phase,'state',state,'attempts',attempts) AS value
        FROM alert_deliveries WHERE workspace_id=a.workspace_id AND alert_id=a.id ORDER BY created_at,id LIMIT 202
    ) d),'[]'::jsonb)::jsonb AS deliveries
FROM operational_alerts a
WHERE a.workspace_id = @workspace_id
    AND (NOT @attention::boolean OR a.state='firing' OR (a.state='event' AND a.acknowledged_at IS NULL))
    AND (sqlc.narg(before_at)::timestamptz IS NULL OR (a.opened_at,a.id)<(sqlc.narg(before_at)::timestamptz,sqlc.narg(before_id)::uuid))
ORDER BY a.opened_at DESC,a.id DESC LIMIT 101;

-- name: AcknowledgeAlert :execrows
UPDATE operational_alerts SET acknowledged_at=COALESCE(acknowledged_at,@acknowledged_at::timestamptz),
    acknowledged_by=COALESCE(acknowledged_by,@acknowledged_by::uuid)
WHERE workspace_id = @workspace_id AND id = @id;

-- name: CancelAcknowledgedAlertDeliveries :exec
UPDATE alert_deliveries SET state='cancelled'
WHERE workspace_id = @workspace_id AND alert_id = @alert_id AND phase<>'resolved' AND state IN ('pending','leased');

-- name: AlertDeliveryContext :one
SELECT a.kind,a.resource_id,a.reason,
    (CASE WHEN @phase::text='resolved' THEN a.resolved_at ELSE a.opened_at END)::timestamptz AS occurred_at,
    a.state,(a.acknowledged_at IS NOT NULL)::boolean AS acknowledged,s.email_enabled,
    (s.encrypted_webhook IS NOT NULL)::boolean AS webhook_enabled,s.revision,s.encrypted_webhook
FROM operational_alerts a JOIN alert_settings s ON s.workspace_id=a.workspace_id
WHERE a.workspace_id = @workspace_id AND a.id = @id;

-- name: NetworkSweepHealth :one
-- Persistent once the sweep has come back at least two minutes into a run of failures.
-- Comparing with its own last visit keeps time the sweep was not running from raising this alone.
SELECT id,(reconcile_failing_since IS NOT NULL)::boolean AS failing,
    COALESCE(checked_at >= reconcile_failing_since + interval '2 minutes',false)::boolean AS persistent
FROM workspace_networks WHERE workspace_id = @workspace_id;
