-- +goose Up
ALTER TABLE workspaces ADD COLUMN alert_scanned_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00';
ALTER TABLE execution_operations ADD COLUMN alert_observed boolean NOT NULL DEFAULT false;
UPDATE execution_operations SET alert_observed=true WHERE state IN ('succeeded','failed','cancelled');
CREATE INDEX operation_alert_candidates ON execution_operations(workspace_id,created_at,id) WHERE NOT alert_observed AND state IN ('failed','needs_attention');

CREATE TABLE alert_settings (
 workspace_id uuid PRIMARY KEY REFERENCES workspaces(id),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 email_enabled boolean NOT NULL DEFAULT false,
 encrypted_webhook bytea
);
CREATE TABLE operational_alerts (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id),
 kind text NOT NULL CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem')),
 resource_id uuid NOT NULL,
 server_id uuid,
 environment_id uuid,
 state text NOT NULL CHECK(state IN ('firing','resolved','event')),
 reason text NOT NULL DEFAULT '' CHECK(reason IN ('','recovered','monitoring_stopped')),
 opened_at timestamptz NOT NULL,
 resolved_at timestamptz,
 acknowledged_at timestamptz,
 acknowledged_by uuid REFERENCES accounts(id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 UNIQUE(workspace_id,id)
);
CREATE UNIQUE INDEX active_alert_identity ON operational_alerts(workspace_id,kind,resource_id) WHERE state='firing';
CREATE UNIQUE INDEX event_alert_identity ON operational_alerts(workspace_id,kind,resource_id) WHERE state='event';
CREATE INDEX alert_inbox ON operational_alerts(workspace_id,opened_at DESC,id DESC);
CREATE TABLE alert_deliveries (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 alert_id uuid NOT NULL,
 phase text NOT NULL CHECK(phase IN ('firing','resolved','event')),
 channel text NOT NULL CHECK(channel IN ('email','webhook')),
 recipient_id uuid NOT NULL,
 settings_revision bigint NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','leased','sent','failed','cancelled')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 next_attempt_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL,
 FOREIGN KEY(workspace_id,alert_id) REFERENCES operational_alerts(workspace_id,id) ON DELETE CASCADE,
 UNIQUE(alert_id,phase,channel,recipient_id)
);
CREATE INDEX alert_delivery_due ON alert_deliveries(next_attempt_at,id) WHERE state IN ('pending','leased');
