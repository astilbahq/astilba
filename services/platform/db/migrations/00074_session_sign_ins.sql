-- +goose Up
-- astilba: not additive: it ends every session so that each one left names its account; an older release would sign in sessions this one neither lists nor signs out, and would mail a sign-out of other sessions as an unexplained change to how the account signs in.
-- A signed-in session records its account and the account version it signed in at, so the
-- account page can list the account's sessions and sign them out. It records when its lineage
-- signed in, which each token that renews it keeps, and when it was last seen, with a short
-- summary of the client that made that request: the browser and operating system families read
-- from its User-Agent, and its network as an IPv4 /24 or IPv6 /48 prefix. Neither the
-- User-Agent nor the address is kept, and the summary goes with the row when the session signs
-- out or expires. A session that is not signed in, such as one waiting for its second factor,
-- records none of it.
--
-- There is no foreign key to accounts: a session insert would then wait for any holder of the
-- account's row, and a logout waiting behind that renewal would wait for the account too.
--
-- A session signed in before this was recorded could be neither listed nor signed out from the
-- account page, so every session ends here and everyone signs in again.
DELETE FROM sessions;
ALTER TABLE sessions ADD COLUMN account_id uuid;
ALTER TABLE sessions ADD COLUMN auth_version bigint;
ALTER TABLE sessions ADD COLUMN signed_in_at timestamptz;
ALTER TABLE sessions ADD COLUMN last_seen_at timestamptz;
ALTER TABLE sessions ADD COLUMN client_browser text;
ALTER TABLE sessions ADD COLUMN client_os text;
ALTER TABLE sessions ADD COLUMN client_network text;
ALTER TABLE sessions ADD CONSTRAINT sessions_sign_in_check CHECK (
    (account_id IS NULL) = (auth_version IS NULL) AND (account_id IS NULL) = (signed_in_at IS NULL)
    AND (account_id IS NULL) = (last_seen_at IS NULL) AND (account_id IS NULL) = (client_browser IS NULL)
    AND (account_id IS NULL) = (client_os IS NULL) AND (account_id IS NOT NULL OR client_network IS NULL)
    AND octet_length(client_browser) BETWEEN 1 AND 16 AND octet_length(client_os) BETWEEN 1 AND 16
    AND octet_length(client_network) BETWEEN 1 AND 64);
CREATE INDEX sessions_account_idx ON sessions (account_id) WHERE account_id IS NOT NULL;
-- Signing out other sessions from the account page is an account security event, and its notice
-- tells the account holder.
ALTER TABLE account_security_events DROP CONSTRAINT account_security_events_kind_check;
ALTER TABLE account_security_events ADD CONSTRAINT account_security_events_kind_check
    CHECK (kind IN ('password_changed','password_reset','mfa_enabled','mfa_disabled','mfa_reset','recovery_codes_regenerated','recovery_code_used','sessions_signed_out'));

-- +goose Down
-- The older release has no such event to tell anyone about, nor any other kind to keep it as.
DELETE FROM account_security_events WHERE kind = 'sessions_signed_out';
ALTER TABLE account_security_events DROP CONSTRAINT account_security_events_kind_check;
ALTER TABLE account_security_events ADD CONSTRAINT account_security_events_kind_check
    CHECK (kind IN ('password_changed','password_reset','mfa_enabled','mfa_disabled','mfa_reset','recovery_codes_regenerated','recovery_code_used'));
DROP INDEX sessions_account_idx;
ALTER TABLE sessions DROP CONSTRAINT sessions_sign_in_check;
ALTER TABLE sessions DROP COLUMN client_network;
ALTER TABLE sessions DROP COLUMN client_os;
ALTER TABLE sessions DROP COLUMN client_browser;
ALTER TABLE sessions DROP COLUMN last_seen_at;
ALTER TABLE sessions DROP COLUMN signed_in_at;
ALTER TABLE sessions DROP COLUMN auth_version;
ALTER TABLE sessions DROP COLUMN account_id;
