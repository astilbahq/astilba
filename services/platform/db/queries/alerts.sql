-- name: AllAlertWebhooks :many
SELECT workspace_id,encrypted_webhook FROM alert_settings WHERE encrypted_webhook IS NOT NULL FOR UPDATE;
-- name: RewrapAlertWebhook :exec
UPDATE alert_settings SET encrypted_webhook=$2 WHERE workspace_id=$1;
