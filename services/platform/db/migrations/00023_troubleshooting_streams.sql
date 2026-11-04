-- +goose Up
ALTER TABLE troubleshooting_sessions ADD COLUMN agent_credential_hash bytea;
ALTER TABLE troubleshooting_sessions ADD COLUMN agent_attached_at timestamptz;
ALTER TABLE troubleshooting_sessions ADD COLUMN browser_attached_at timestamptz;

-- +goose Down
ALTER TABLE troubleshooting_sessions DROP COLUMN browser_attached_at;
ALTER TABLE troubleshooting_sessions DROP COLUMN agent_attached_at;
ALTER TABLE troubleshooting_sessions DROP COLUMN agent_credential_hash;
