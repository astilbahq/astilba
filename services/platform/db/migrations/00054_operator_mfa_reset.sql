-- +goose Up
-- astilba: not additive: an older release would email an operator's reset as an unexplained change, without its reason or the password reset it needs.
-- An operator's reset of an account's two-factor authentication is an account security event of
-- its own, kept with the operator's reason. That reason is the record of why the operator acted,
-- so it is kept as long as the event is, and only that kind has one.
ALTER TABLE account_security_events ADD COLUMN reason text;
ALTER TABLE account_security_events DROP CONSTRAINT account_security_events_kind_check;
ALTER TABLE account_security_events ADD CONSTRAINT account_security_events_kind_check
    CHECK (kind IN ('password_changed','password_reset','mfa_enabled','mfa_disabled','mfa_reset','recovery_codes_regenerated','recovery_code_used'));
ALTER TABLE account_security_events ADD CONSTRAINT account_security_events_reason_check
    CHECK ((kind = 'mfa_reset') = (reason IS NOT NULL) AND octet_length(reason) BETWEEN 1 AND 500);

-- +goose Down
-- An older release knows no operator reset, so each is kept as two-factor authentication turned
-- off, and its reason is lost with the column.
ALTER TABLE account_security_events DROP CONSTRAINT account_security_events_reason_check;
UPDATE account_security_events SET kind = 'mfa_disabled' WHERE kind = 'mfa_reset';
ALTER TABLE account_security_events DROP CONSTRAINT account_security_events_kind_check;
ALTER TABLE account_security_events ADD CONSTRAINT account_security_events_kind_check
    CHECK (kind IN ('password_changed','password_reset','mfa_enabled','mfa_disabled','recovery_codes_regenerated','recovery_code_used'));
ALTER TABLE account_security_events DROP COLUMN reason;
