-- +goose Up
-- Each instance reports the custody stage its release completes, so converting values to workspace
-- keys records its stage only while no instance seen lately runs a release that seals under the
-- installation keyring. NULL is a release from before instances reported it, which may.
--
-- Additive: the previous release does not read it, and its rows stay NULL.
ALTER TABLE custody_instances ADD COLUMN stage integer;

-- Whether a sealed value is in a v2 envelope, under a workspace or installation key rather than the
-- installation keyring. Every envelope is encoded with its version first and no space, so the
-- prefix is exact, and reading it never fails on a value it cannot parse: such a value counts as
-- not converted, and converting it names it. It has no settings of its own, so queries and index
-- predicates inline it; its operators are pg_catalog's, which every search path reads first.
-- +goose StatementBegin
CREATE FUNCTION custody_value_converted(value bytea) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT substring(value FROM 1 FOR 13) = '\x7b2276657273696f6e223a322c'::bytea
$$;
-- +goose StatementEnd

-- The values still sealed under the keyring, which converting reads and recording stage 4 counts
-- while it holds these tables against writes: the count reads only these, however many values are
-- stored. Each index's predicate is the condition the conversion queries state for its column,
-- which tests a column for NULL only where it may be NULL: the planner drops that test from a
-- query on a NOT NULL column, and an index whose predicate kept it would never match.
-- `astilba migrate` runs only with management stopped (docs/management-upgrades.md), so building
-- them blocks no writers.
CREATE INDEX secret_versions_under_keyring ON secret_versions(workspace_id, id) WHERE encrypted_value IS NOT NULL AND custody_value_converted(encrypted_value) IS NOT TRUE;
CREATE INDEX git_connections_under_keyring ON git_connections(workspace_id, id) WHERE custody_value_converted(encrypted_access) IS NOT TRUE;
CREATE INDEX github_app_authorizations_under_keyring ON github_app_authorizations(workspace_id, id) WHERE custody_value_converted(encrypted_access) IS NOT TRUE;
CREATE INDEX registries_under_keyring ON registries(workspace_id, id) WHERE custody_value_converted(encrypted_access) IS NOT TRUE;
CREATE INDEX build_secret_versions_under_keyring ON build_secret_versions(workspace_id, id) WHERE custody_value_converted(encrypted_value) IS NOT TRUE;
CREATE INDEX backup_destinations_under_keyring ON backup_destinations(workspace_id, id) WHERE custody_value_converted(encrypted_access) IS NOT TRUE;
CREATE INDEX backup_policies_under_keyring ON backup_policies(workspace_id, id) WHERE custody_value_converted(encrypted_access) IS NOT TRUE;
CREATE INDEX alert_settings_under_keyring ON alert_settings(workspace_id) WHERE encrypted_webhook IS NOT NULL AND custody_value_converted(encrypted_webhook) IS NOT TRUE;
CREATE INDEX accounts_totp_under_keyring ON accounts(id) WHERE totp_secret IS NOT NULL AND custody_value_converted(totp_secret) IS NOT TRUE;
CREATE INDEX accounts_totp_pending_under_keyring ON accounts(id) WHERE totp_pending IS NOT NULL AND custody_value_converted(totp_pending) IS NOT TRUE;
INSERT INTO schema_additive_migrations (version) VALUES (65);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 65;
DROP INDEX accounts_totp_pending_under_keyring;
DROP INDEX accounts_totp_under_keyring;
DROP INDEX alert_settings_under_keyring;
DROP INDEX backup_policies_under_keyring;
DROP INDEX backup_destinations_under_keyring;
DROP INDEX build_secret_versions_under_keyring;
DROP INDEX registries_under_keyring;
DROP INDEX github_app_authorizations_under_keyring;
DROP INDEX git_connections_under_keyring;
DROP INDEX secret_versions_under_keyring;
DROP FUNCTION custody_value_converted(bytea);
ALTER TABLE custody_instances DROP COLUMN stage;
