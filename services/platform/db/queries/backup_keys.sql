-- name: InsertBackupStorageAccount :exec
INSERT INTO backup_storage_accounts(id,workspace_id,name,aws_account,admin_arn,boundary,encrypted_credential)
VALUES(@id,@workspace_id,@name,@aws_account,@admin_arn,@boundary,@encrypted_credential);

-- name: ListBackupStorageAccounts :many
SELECT id,name,aws_account,admin_arn,boundary,generation,peers,checked_at,check_error,report,created_at
FROM backup_storage_accounts WHERE workspace_id = @workspace_id ORDER BY name,id LIMIT 100;

-- name: BackupStorageAccount :one
SELECT id,workspace_id,name,aws_account,admin_arn,boundary,generation,peers,encrypted_credential,report
FROM backup_storage_accounts WHERE workspace_id = @workspace_id AND id = @id;

-- name: ReplaceBackupStorageAccountCredential :execrows
-- Only the generation the owner checked against is replaced, so two replacements can't interleave.
UPDATE backup_storage_accounts SET admin_arn = @admin_arn,boundary = @boundary,encrypted_credential = @encrypted_credential,generation=generation+1
WHERE workspace_id = @workspace_id AND id = @id AND generation = @generation;

-- name: AddBackupStorageAccountPeer :execrows
UPDATE backup_storage_accounts SET peers=array_append(peers,@peer::uuid)
WHERE workspace_id = @workspace_id AND id = @id AND NOT (@peer::uuid = ANY(peers)) AND cardinality(peers) < 16;

-- name: ClaimDueBackupStorageAccountCheck :one
-- unscoped: the account check sweeps every workspace. Claiming moves the account's check a day
-- on before it runs, so a check that fails can't hold the head of the scan; its result is recorded
-- in the account's own workspace.
UPDATE backup_storage_accounts SET checked_at=clock_timestamp()
WHERE id=(SELECT a.id FROM backup_storage_accounts a
    WHERE a.checked_at IS NULL OR a.checked_at < clock_timestamp() - interval '1 day'
    ORDER BY a.checked_at NULLS FIRST,a.id LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING workspace_id,id;

-- name: RequestBackupStorageAccountCheck :exec
UPDATE backup_storage_accounts SET checked_at=NULL WHERE workspace_id = @workspace_id AND id = @id;

-- name: RecordBackupStorageAccountCheck :exec
UPDATE backup_storage_accounts SET check_error = sqlc.narg(check_error),report = @report
WHERE workspace_id = @workspace_id AND id = @id AND generation = @generation;

-- name: InsertBackupStorageKey :exec
INSERT INTO backup_storage_keys(id,workspace_id,account_id,destination_id,holder_server_id,purpose,scope,user_name,recovery_request,recovery_policy_id,recovery_snapshot)
VALUES(@id,@workspace_id,@account_id,@destination_id,@holder_server_id,@purpose,@scope,@user_name,sqlc.narg(recovery_request),sqlc.narg(recovery_policy_id),sqlc.narg(recovery_snapshot));

-- name: InsertFoundBackupStorageKey :exec
-- A user the account check found with no row, latched for the sweep to revoke.
INSERT INTO backup_storage_keys(id,workspace_id,account_id,purpose,user_name,found_tags,state,mint_deadline)
VALUES(@id,@workspace_id,@account_id,'found',@user_name,@found_tags,'revoking',clock_timestamp() - interval '5 minutes')
ON CONFLICT (user_name) DO NOTHING;

-- name: BackupStorageKey :one
SELECT * FROM backup_storage_keys WHERE workspace_id = @workspace_id AND id = @id;

-- name: ActivateBackupStorageKey :execrows
-- A key becomes active only while it is still minting and its holder isn't revoked: the sweep latches
-- a key before it calls AWS, so a request that outlived its deadline can't activate a key being deleted.
UPDATE backup_storage_keys k SET state='active',activated_at=clock_timestamp(),access_key_id = @access_key_id,encrypted_secret = @encrypted_secret,installs = @installs,recovery_id = sqlc.narg(recovery_id)
FROM servers s
WHERE k.workspace_id = @workspace_id AND k.id = @id AND k.state = 'minting'
    AND s.workspace_id = k.workspace_id AND s.id = k.holder_server_id AND NOT s.revoked;

-- name: LatchBackupStorageKey :execrows
-- revoking is a latch: nothing moves a key back. The sweep waits until a minting key's deadline has
-- passed by five minutes, so it can't race a create call still in flight.
UPDATE backup_storage_keys SET state='revoking',last_error=COALESCE(sqlc.narg(last_error),last_error),
    next_attempt_at=GREATEST(clock_timestamp(),mint_deadline + interval '5 minutes')
WHERE workspace_id = @workspace_id AND id = @id AND state IN ('minting','active');

-- name: LatchServerBackupStorageKeys :many
-- A revoked server's keys, except those handed over at its current revocation.
UPDATE backup_storage_keys k SET state='revoking',next_attempt_at=GREATEST(clock_timestamp(),k.mint_deadline + interval '5 minutes')
FROM servers s
WHERE k.workspace_id = @workspace_id AND k.holder_server_id = @server_id AND k.state IN ('minting','active')
    AND s.workspace_id = k.workspace_id AND s.id = k.holder_server_id
    AND NOT (s.revoked AND k.handed_over IS NOT NULL AND k.handed_over = s.revocations)
RETURNING k.id;

-- name: HandOverServerBackupStorageKeys :many
-- Binds a revoked server's server keys to its current revocation, for a workspace transfer.
UPDATE backup_storage_keys k SET handed_over=s.revocations
FROM servers s
WHERE k.workspace_id = @workspace_id AND k.holder_server_id = @server_id AND k.purpose = 'server' AND k.state = 'active'
    AND s.workspace_id = k.workspace_id AND s.id = k.holder_server_id AND s.revoked
RETURNING k.id;

-- name: ClaimDueBackupStorageKey :one
-- unscoped: the revocation sweep visits every workspace. Claiming moves the key's next attempt on,
-- so a pass that dies keeps its place without retrying at once; the result is recorded in the
-- key's own workspace.
UPDATE backup_storage_keys SET next_attempt_at=clock_timestamp() + interval '2 minutes'
WHERE id=(SELECT k.id FROM backup_storage_keys k
    WHERE k.state = 'revoking' AND k.next_attempt_at <= clock_timestamp()
        AND k.mint_deadline + interval '5 minutes' <= clock_timestamp()
        AND k.workspace_id <> ALL(@stalled_workspaces::uuid[])
    ORDER BY k.next_attempt_at,k.id LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING workspace_id,id,account_id,user_name;

-- name: RecordBackupStorageKeyRevoked :one
UPDATE backup_storage_keys SET state='revoked',revoked_at=clock_timestamp(),last_error=NULL
WHERE workspace_id = @workspace_id AND id = @id AND state = 'revoking'
RETURNING destination_id;

-- name: RecordBackupStorageKeyFailure :exec
-- Failures back off to fifteen minutes and retry for ever.
UPDATE backup_storage_keys SET attempts=attempts+1,last_error = @last_error,
    next_attempt_at=clock_timestamp() + LEAST(interval '15 minutes', interval '30 seconds' * power(2, LEAST(attempts,10)))
WHERE workspace_id = @workspace_id AND id = @id AND state = 'revoking';

-- name: RetryBackupStorageKeys :execrows
UPDATE backup_storage_keys SET next_attempt_at=GREATEST(clock_timestamp(),mint_deadline + interval '5 minutes')
WHERE workspace_id = @workspace_id AND destination_id = @destination_id AND state = 'revoking';

-- name: RetryAccountBackupStorageKeys :execrows
-- A replaced admin credential retries the account's failing revocations at once.
UPDATE backup_storage_keys SET next_attempt_at=GREATEST(clock_timestamp(),mint_deadline + interval '5 minutes')
WHERE workspace_id = @workspace_id AND account_id = @account_id AND state = 'revoking';

-- name: DeleteBackupPlaceholder :execrows
-- A destination that never had an active key, once none of its keys is left to revoke, goes with
-- its key rows. None of them was ever delivered, and no policy can use a placeholder.
WITH placeholder AS (
    SELECT d.id FROM backup_destinations d
    WHERE d.workspace_id = @workspace_id AND d.id = @destination_id AND d.account_id IS NOT NULL
        AND NOT EXISTS(SELECT 1 FROM backup_storage_keys k WHERE k.workspace_id = d.workspace_id AND k.destination_id = d.id AND (k.activated_at IS NOT NULL OR k.state <> 'revoked'))
        AND NOT EXISTS(SELECT 1 FROM backup_policies p WHERE p.workspace_id = d.workspace_id AND p.configuration->>'destinationId' = d.id::text)
), keys AS (
    DELETE FROM backup_storage_keys k USING placeholder p WHERE k.workspace_id = @workspace_id AND k.destination_id = p.id
)
DELETE FROM backup_destinations d USING placeholder p WHERE d.workspace_id = @workspace_id AND d.id = p.id;

-- name: CurrentBackupStorageKey :one
-- A managed destination's current key: its newest active server key.
SELECT id,holder_server_id,access_key_id,encrypted_secret FROM backup_storage_keys
WHERE workspace_id = @workspace_id AND destination_id = @destination_id AND purpose = 'server' AND state = 'active'
ORDER BY activated_at DESC,id DESC LIMIT 1;

-- name: RecoveryBackupStorageKey :one
SELECT id,holder_server_id,access_key_id,encrypted_secret FROM backup_storage_keys
WHERE workspace_id = @workspace_id AND recovery_id = @recovery_id AND purpose = 'recovery' AND state = 'active';

-- name: RecoveryStartKey :one
-- The newest start of a recovery with this request key.
SELECT id,state,recovery_id,recovery_policy_id,recovery_snapshot,holder_server_id FROM backup_storage_keys
WHERE workspace_id = @workspace_id AND purpose = 'recovery' AND recovery_request = @recovery_request
ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: LatchEndedRecoveryKeys :many
-- unscoped: the sweep revokes every recovery key whose operation has ended, however it ended.
UPDATE backup_storage_keys k SET state='revoking',next_attempt_at=clock_timestamp()
FROM backup_recoveries r JOIN execution_operations o ON o.workspace_id = r.workspace_id AND o.id = r.operation_id
WHERE k.purpose = 'recovery' AND k.state = 'active' AND r.workspace_id = k.workspace_id AND r.id = k.recovery_id
    AND o.state IN ('succeeded','failed','cancelled')
RETURNING k.workspace_id,k.id;

-- name: DestinationServerKeys :many
-- unscoped: the sweep's view of every managed destination's active server keys, newest first.
SELECT k.workspace_id,k.destination_id,k.id,k.holder_server_id,k.activated_at,k.installs
FROM backup_storage_keys k
WHERE k.purpose = 'server' AND k.state = 'active'
    AND EXISTS(SELECT 1 FROM backup_storage_keys o WHERE o.workspace_id = k.workspace_id AND o.destination_id = k.destination_id AND o.purpose = 'server' AND o.state = 'active' AND o.id <> k.id)
ORDER BY k.workspace_id,k.destination_id,k.activated_at DESC,k.id DESC
LIMIT 1000;

-- name: PolicyReportedGenerations :many
SELECT id,COALESCE((status->>'generation')::bigint,0)::bigint AS reported,(configuration->>'generation')::bigint AS configured
FROM backup_policies WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[]);

-- name: ClaimDueBackupRemint :one
-- unscoped: a managed destination whose server isn't revoked, that once had a key and now has no
-- active or minting one. Claiming pushes its next attempt on before the mint runs.
UPDATE backup_destinations SET remint_next_at=clock_timestamp() + interval '10 minutes'
WHERE id=(SELECT d.id FROM backup_destinations d
    JOIN servers s ON s.workspace_id = d.workspace_id AND s.id = d.server_id AND NOT s.revoked
    WHERE d.account_id IS NOT NULL AND (d.remint_next_at IS NULL OR d.remint_next_at <= clock_timestamp())
        AND EXISTS(SELECT 1 FROM backup_storage_keys k WHERE k.workspace_id = d.workspace_id AND k.destination_id = d.id AND k.purpose = 'server' AND k.activated_at IS NOT NULL)
        AND NOT EXISTS(SELECT 1 FROM backup_storage_keys k WHERE k.workspace_id = d.workspace_id AND k.destination_id = d.id AND k.purpose = 'server' AND k.state IN ('minting','active'))
    ORDER BY d.remint_next_at NULLS FIRST,d.id LIMIT 1 FOR UPDATE OF d SKIP LOCKED)
RETURNING workspace_id,id;

-- name: RecordBackupRemintFailure :exec
-- A failed re-mint backs off to an hour.
UPDATE backup_destinations SET remint_attempts=remint_attempts+1,remint_error = @remint_error,
    remint_next_at=clock_timestamp() + LEAST(interval '1 hour', interval '1 minute' * power(2, LEAST(remint_attempts,10)))
WHERE workspace_id = @workspace_id AND id = @id;

-- name: RecordBackupRemint :exec
UPDATE backup_destinations SET remint_attempts=0,remint_error=NULL,remint_next_at=NULL
WHERE workspace_id = @workspace_id AND id = @id;

-- name: RetryBackupRemint :exec
-- The owner's retry starts a failing re-mint on the next pass.
UPDATE backup_destinations SET remint_next_at=NULL
WHERE workspace_id = @workspace_id AND id = @id AND account_id IS NOT NULL AND remint_error IS NOT NULL;

-- name: ManagedBackupDestination :one
SELECT d.configuration,d.server_id,d.account_id FROM backup_destinations d
WHERE d.workspace_id = @workspace_id AND d.id = @id AND d.account_id IS NOT NULL;

-- name: DestinationPolicyGenerations :many
-- The policies on a destination, for a re-mint's generation bump.
SELECT id,environment_id,configuration,COALESCE((status->>'generation')::bigint,0)::bigint AS reported
FROM backup_policies WHERE workspace_id = @workspace_id AND configuration->>'destinationId' = @destination_id::text
ORDER BY id FOR UPDATE;

-- name: AccountBackupStorageKeys :many
SELECT id,purpose,state,user_name,access_key_id,activated_at,revoked_at,holder_server_id
FROM backup_storage_keys WHERE workspace_id = @workspace_id AND account_id = @account_id;

-- name: DestinationBackupStorageKeys :many
-- The keys of a workspace's managed destinations, for their views.
SELECT k.destination_id,k.id,k.purpose,k.state,k.user_name,k.holder_server_id,k.activated_at,k.revoked_at,k.attempts,k.last_error,
    (k.handed_over IS NOT NULL AND s.revoked AND k.handed_over = s.revocations)::bool AS handed_over
FROM backup_storage_keys k JOIN servers s ON s.workspace_id = k.workspace_id AND s.id = k.holder_server_id
WHERE k.workspace_id = @workspace_id AND k.destination_id IS NOT NULL
ORDER BY k.destination_id,k.created_at DESC,k.id DESC LIMIT 2000;

-- name: LiveServerKeyExists :one
SELECT EXISTS(SELECT 1 FROM backup_storage_keys WHERE workspace_id = @workspace_id AND destination_id = @destination_id AND purpose = 'server' AND state IN ('minting','active'))::bool;

-- name: UpdateBackupPolicyConfiguration :exec
-- A re-mint's generation bump. The policy keeps its sealed access.
UPDATE backup_policies SET configuration = @configuration WHERE workspace_id = @workspace_id AND id = @id;

-- name: PendingKeyInstalls :many
-- unscoped: the policies whose install would carry a re-minted key and that their server hasn't
-- installed yet, still at the generation the re-mint wrote. A later owner's save installs the key
-- too, so it ends the wait.
SELECT k.workspace_id,k.id AS key_id,p.id AS policy_id,p.environment_id,(p.configuration->>'generation')::bigint AS generation
FROM backup_storage_keys k
JOIN backup_policies p ON p.workspace_id = k.workspace_id
    AND k.installs @> jsonb_build_array(jsonb_build_object('policy', p.id::text, 'generation', (p.configuration->>'generation')::bigint))
WHERE k.purpose = 'server' AND k.state = 'active' AND jsonb_array_length(k.installs) > 0
    AND COALESCE((p.status->>'generation')::bigint,0) < (p.configuration->>'generation')::bigint
ORDER BY k.activated_at,k.id,p.id
LIMIT 20;

-- name: AlertingBackupStorageAccounts :many
-- The accounts whose latest check failed or found something the owner should look at.
SELECT id FROM backup_storage_accounts
WHERE workspace_id = @workspace_id AND (check_error IS NOT NULL OR COALESCE((report->>'alert')::bool,false));

-- name: RelatchRevokedBackupStorageKey :execrows
-- A key recorded revoked whose user the account check still finds an hour later is revoked again.
-- Only the account's records say a user is gone; IAM is the source of truth for whether it is.
UPDATE backup_storage_keys SET state='revoking',revoked_at=NULL,next_attempt_at=clock_timestamp()
WHERE workspace_id = @workspace_id AND id = @id AND state = 'revoked';

-- name: WorkspaceServerStates :many
SELECT id,revoked FROM servers WHERE workspace_id = @workspace_id;

-- name: LatchRevokedHolderBackupStorageKeys :execrows
-- An imported workspace's active keys whose holder the archive records revoked, other than those
-- handed over at that revocation, are revoked by the importer too.
UPDATE backup_storage_keys k SET state='revoking',next_attempt_at=clock_timestamp()
FROM servers s
WHERE k.workspace_id = @workspace_id AND k.state = 'active' AND s.workspace_id = k.workspace_id AND s.id = k.holder_server_id
    AND s.revoked AND (k.handed_over IS NULL OR k.handed_over <> s.revocations);
