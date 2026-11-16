-- name: LegacyWorkspaceValues :many
-- One workspace's values still sealed under the installation keyring, after (after_kind,
-- after_id), for `custody convert`, with the environment of each that belongs to one. With
-- environments, an environment's values not yet under its own key are included too.
-- Recovery checkpoints are never converted: hosts hold them bound by digest.
SELECT v.kind::text AS kind, v.id::uuid AS id, v.sealed::bytea AS sealed, COALESCE(v.environment_id, '00000000-0000-0000-0000-000000000000')::uuid AS environment_id FROM (
    SELECT 'secret-version' AS kind, sv.id, sv.encrypted_value AS sealed, s.environment_id FROM secret_versions sv
        JOIN secrets s ON s.workspace_id = sv.workspace_id AND s.id = sv.secret_id
        WHERE sv.workspace_id = @workspace::uuid AND sv.encrypted_value IS NOT NULL
            AND (custody_value_converted(sv.encrypted_value) IS NOT TRUE OR @environments::boolean AND substring(sv.encrypted_value FROM 1 FOR 35) <> '\x7b2276657273696f6e223a322c2273636f7065223a22656e7669726f6e6d656e74222c'::bytea)
    UNION ALL SELECT 'git', id, encrypted_access, NULL FROM git_connections WHERE workspace_id = @workspace::uuid AND custody_value_converted(encrypted_access) IS NOT TRUE
    UNION ALL SELECT 'github-authorization', id, encrypted_access, NULL FROM github_app_authorizations WHERE workspace_id = @workspace::uuid AND custody_value_converted(encrypted_access) IS NOT TRUE
    UNION ALL SELECT 'registry', id, encrypted_access, NULL FROM registries WHERE workspace_id = @workspace::uuid AND custody_value_converted(encrypted_access) IS NOT TRUE
    UNION ALL SELECT 'build-secret', id, encrypted_value, NULL FROM build_secret_versions WHERE workspace_id = @workspace::uuid AND custody_value_converted(encrypted_value) IS NOT TRUE
    UNION ALL SELECT 'backup-destination', id, encrypted_access, NULL FROM backup_destinations WHERE workspace_id = @workspace::uuid AND custody_value_converted(encrypted_access) IS NOT TRUE
    UNION ALL SELECT 'backup-policy', id, encrypted_access, NULL FROM backup_policies WHERE workspace_id = @workspace::uuid AND custody_value_converted(encrypted_access) IS NOT TRUE
    UNION ALL SELECT 'alert-webhook', workspace_id, encrypted_webhook, NULL FROM alert_settings WHERE workspace_id = @workspace::uuid AND encrypted_webhook IS NOT NULL AND custody_value_converted(encrypted_webhook) IS NOT TRUE
) v
WHERE (v.kind, v.id) > (@after_kind::text, @after_id::uuid)
ORDER BY v.kind, v.id LIMIT @batch;

-- Each conversion stores a value only if it is still the one converted, so a value written since
-- is kept, and converted by a later run if it needs to be.

-- name: ConvertSecretVersion :execrows
UPDATE secret_versions SET encrypted_value = @converted WHERE workspace_id = @workspace_id AND id = @id AND encrypted_value = @previous;

-- name: ConvertGitAccess :execrows
UPDATE git_connections SET encrypted_access = @converted WHERE workspace_id = @workspace_id AND id = @id AND encrypted_access = @previous;

-- name: ConvertGitHubAuthorization :execrows
UPDATE github_app_authorizations SET encrypted_access = @converted WHERE workspace_id = @workspace_id AND id = @id AND encrypted_access = @previous;

-- name: ConvertRegistryAccess :execrows
UPDATE registries SET encrypted_access = @converted WHERE workspace_id = @workspace_id AND id = @id AND encrypted_access = @previous;

-- name: ConvertBuildSecret :execrows
UPDATE build_secret_versions SET encrypted_value = @converted WHERE workspace_id = @workspace_id AND id = @id AND encrypted_value = @previous;

-- name: ConvertBackupDestinationAccess :execrows
UPDATE backup_destinations SET encrypted_access = @converted WHERE workspace_id = @workspace_id AND id = @id AND encrypted_access = @previous;

-- name: ConvertBackupPolicyAccess :execrows
UPDATE backup_policies SET encrypted_access = @converted WHERE workspace_id = @workspace_id AND id = @id AND encrypted_access = @previous;

-- name: ConvertAlertWebhook :execrows
UPDATE alert_settings SET encrypted_webhook = @converted WHERE workspace_id = @workspace_id AND encrypted_webhook = @previous;

-- name: LegacyTOTPSecrets :many
-- unscoped: second factors belong to accounts, which are the installation's, not a workspace's.
SELECT id, totp_secret, totp_pending FROM accounts
WHERE id > @after AND (totp_secret IS NOT NULL AND custody_value_converted(totp_secret) IS NOT TRUE
    OR totp_pending IS NOT NULL AND custody_value_converted(totp_pending) IS NOT TRUE)
ORDER BY id LIMIT @batch;

-- name: ConvertTOTPSecret :execrows
UPDATE accounts SET totp_secret = @converted WHERE id = @id AND totp_secret = @previous;

-- name: ConvertTOTPPending :execrows
UPDATE accounts SET totp_pending = @converted WHERE id = @id AND totp_pending = @previous;

-- name: ConversionWorkspaces :many
-- unscoped: converting goes through every workspace, in ID order from after.
SELECT id FROM workspaces WHERE id > @after ORDER BY id LIMIT @batch;

-- name: LegacyValueCounts :many
-- unscoped: how many values each kind stores across the installation, how many of them are
-- still sealed under the keyring, and how many of an environment's values are not under its own
-- key. Counting reads every stored value.
SELECT v.kind::text AS kind, count(*)::bigint AS stored,
    count(*) FILTER (WHERE custody_value_converted(v.sealed) IS NOT TRUE)::bigint AS remaining,
    count(*) FILTER (WHERE v.kind = 'secret-version' AND substring(v.sealed FROM 1 FOR 35) IS DISTINCT FROM '\x7b2276657273696f6e223a322c2273636f7065223a22656e7669726f6e6d656e74222c'::bytea)::bigint AS outside
FROM (
    SELECT 'secret-version' AS kind, encrypted_value AS sealed FROM secret_versions WHERE encrypted_value IS NOT NULL
    UNION ALL SELECT 'git', encrypted_access FROM git_connections
    UNION ALL SELECT 'github-authorization', encrypted_access FROM github_app_authorizations
    UNION ALL SELECT 'registry', encrypted_access FROM registries
    UNION ALL SELECT 'build-secret', encrypted_value FROM build_secret_versions
    UNION ALL SELECT 'backup-destination', encrypted_access FROM backup_destinations
    UNION ALL SELECT 'backup-policy', encrypted_access FROM backup_policies
    UNION ALL SELECT 'alert-webhook', encrypted_webhook FROM alert_settings WHERE encrypted_webhook IS NOT NULL
    UNION ALL SELECT 'totp', totp_secret FROM accounts WHERE totp_secret IS NOT NULL
    UNION ALL SELECT 'totp-pending', totp_pending FROM accounts WHERE totp_pending IS NOT NULL
) v
GROUP BY v.kind ORDER BY v.kind;

-- name: HoldSealedValueTables :exec
-- Holds every table with converted values against writes, not reads, until the transaction ends,
-- so no value can be stored under the keyring between counting them and recording the stage.
LOCK TABLE secret_versions, git_connections, github_app_authorizations, registries, build_secret_versions,
    backup_destinations, backup_policies, alert_settings, accounts IN SHARE MODE;

-- name: InstancesBelowStage :one
-- unscoped: instances seen in the last five minutes whose release completes a stage before
-- @stage, or reports none, as a release from before instances reported it does.
SELECT count(*)::bigint FROM custody_instances
WHERE seen_at > now() - interval '5 minutes' AND (stage IS NULL OR stage < @stage::integer);

-- name: LimitLockWait :exec
-- Bounds how long the transaction waits for any lock, so a command refuses rather than queueing
-- writers behind it.
SELECT set_config('lock_timeout', @wait::text, true);
