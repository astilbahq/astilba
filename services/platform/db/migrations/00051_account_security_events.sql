-- +goose Up
-- Changes to how an account signs in, kept for the account itself rather than a workspace. Each
-- one emails the account holder, through a job queued in the same transaction. Like audit
-- history, they are kept; only the account holder's own credential changes add them.
CREATE TABLE account_security_events (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('password_changed','password_reset','mfa_enabled','mfa_disabled','recovery_codes_regenerated','recovery_code_used')),
    created_at timestamptz NOT NULL DEFAULT now()
);
-- An older release neither reads nor writes the table.
INSERT INTO schema_additive_migrations (version) VALUES (51);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 51;
DROP TABLE account_security_events;
