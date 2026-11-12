-- +goose Up
-- Whether account approves for ws now: it holds the role a grant needs, an owner or, unless
-- owner is set, an operator, with the authentication version it approved with. Every check of an
-- invitation, enrollment code or standing automation policy uses this.
CREATE FUNCTION policy_approver_holds(ws uuid, account uuid, approved_version bigint, owner boolean) RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE
RETURN EXISTS (
    SELECT 1 FROM accounts a JOIN memberships m ON m.account_id=a.id
    WHERE a.id=account AND a.auth_version=approved_version AND m.workspace_id=ws
        AND (m.role='owner' OR (NOT owner AND m.role='operator'))
);

-- An invitation or an enrollment code acts for its workspace on the say-so of the owner who
-- issued it, so it counts only while that owner still is one, with the same authentication
-- version. Codes and invitations issued before this records no issuer: they are withdrawn, and an
-- owner issues them again. An invitation lasts a day and a code ten minutes.
DELETE FROM invitations;
ALTER TABLE invitations
    ADD COLUMN issuer_id uuid NOT NULL REFERENCES accounts(id),
    ADD COLUMN issuer_auth_version bigint NOT NULL;
CREATE INDEX invitations_issuer ON invitations(workspace_id, issuer_id);

UPDATE servers SET enrollment_hash=NULL, enrollment_expires=NULL WHERE enrollment_hash IS NOT NULL;
ALTER TABLE servers
    ADD COLUMN enrollment_issuer_id uuid REFERENCES accounts(id),
    ADD COLUMN enrollment_issuer_auth_version bigint,
    ADD CONSTRAINT servers_enrollment_issuer CHECK (
        enrollment_hash IS NULL OR (enrollment_issuer_id IS NOT NULL AND enrollment_issuer_auth_version IS NOT NULL));

-- Before this, removing or demoting an owner withdrew nothing: their grants only stopped working
-- while they lacked the role, and came back if they got it again. End those now, so that making
-- such a person an owner again restores nothing.
CREATE INDEX automation_credentials_creator ON automation_credentials(workspace_id, creator_id) WHERE revoked_at IS NULL;
UPDATE automation_credentials c SET revoked_at=now()
WHERE c.revoked_at IS NULL AND c.expires_at>now()
    AND NOT EXISTS(SELECT 1 FROM memberships m WHERE m.workspace_id=c.workspace_id AND m.account_id=c.creator_id AND m.role='owner');
UPDATE preview_policies p SET enabled=false
WHERE p.enabled AND NOT policy_approver_holds(p.workspace_id, p.actor_id, p.auth_version, true);
UPDATE preview_requests r SET cleanup_actor=NULL, cleanup_auth_version=NULL, cleanup_controller=NULL
WHERE r.cleanup_actor IS NOT NULL AND NOT policy_approver_holds(r.workspace_id, r.cleanup_actor, r.cleanup_auth_version, true);

-- A standing automation policy acts on its approver's say-so. It records the approver's
-- authentication version when they approved it, and counts only while they still hold the role
-- it needs with that version: an owner for Git imports and anything that deploys, an owner or
-- operator for automatic builds. A NULL version is an approval that was withdrawn, when the
-- approver lost that role, by management recovery or by import; only a new approval sets it
-- again. A build target also records whether an owner approved it, which running it on demand
-- with an automation credential needs, and which losing ownership withdraws while an operator's
-- automatic builds go on. An approval counts from here only if its approver holds the role now.
ALTER TABLE git_sources ADD COLUMN policy_auth_version bigint;
ALTER TABLE build_targets ADD COLUMN policy_auth_version bigint, ADD COLUMN policy_owner boolean NOT NULL DEFAULT false;
ALTER TABLE pipeline_auto_deployments ADD COLUMN actor_auth_version bigint;
UPDATE git_sources g SET policy_auth_version=a.auth_version FROM accounts a
WHERE a.id=g.policy_actor AND policy_approver_holds(g.workspace_id, g.policy_actor, a.auth_version, true);
UPDATE build_targets b SET policy_auth_version=a.auth_version,
    policy_owner=policy_approver_holds(b.workspace_id, b.policy_actor, a.auth_version, true)
FROM accounts a
WHERE a.id=b.policy_actor
    AND policy_approver_holds(b.workspace_id, b.policy_actor, a.auth_version, COALESCE((b.configuration->>'autoDeploy')::boolean,false));
UPDATE pipeline_auto_deployments d SET actor_auth_version=a.auth_version FROM accounts a
WHERE a.id=d.actor_id AND policy_approver_holds(d.workspace_id, d.actor_id, a.auth_version, true);
CREATE INDEX git_sources_policy_actor ON git_sources(workspace_id, policy_actor);
CREATE INDEX build_targets_policy_actor ON build_targets(workspace_id, policy_actor);

-- A fork commit's preview approval counts only while the owner who gave it still is one with the
-- same authentication version. Approvals given before this record no approver and are withdrawn.
ALTER TABLE preview_requests ADD COLUMN approved_by uuid REFERENCES accounts(id), ADD COLUMN approved_auth_version bigint;
UPDATE preview_requests SET approved_head='', approved_repository=0, approved_generation=0 WHERE approved_head<>'';
CREATE INDEX preview_requests_approved_by ON preview_requests(workspace_id, approved_by) WHERE approved_by IS NOT NULL;
CREATE INDEX preview_requests_cleanup_actor ON preview_requests(workspace_id, cleanup_actor) WHERE cleanup_actor IS NOT NULL;

-- +goose Down
-- The earlier release checks only the approver's role, so pause what no longer has an approval
-- before the record of it goes: otherwise an approval withdrawn by a credential change, or by an
-- owner's demotion to operator, would run again.
UPDATE git_sources SET status='approval_required', configuration=configuration || '{"autoDeploy":false}'::jsonb
WHERE NOT policy_approver_holds(workspace_id, policy_actor, policy_auth_version, true);
UPDATE build_targets SET configuration=configuration || '{"autoBuild":false,"autoDeploy":false}'::jsonb
WHERE NOT policy_approver_holds(workspace_id, policy_actor, policy_auth_version, COALESCE((configuration->>'autoDeploy')::boolean,false));
UPDATE pipeline_auto_deployments SET state='blocked', note='The owner who approved automatic deployment no longer approves it.'
WHERE state='pending' AND NOT policy_approver_holds(workspace_id, actor_id, actor_auth_version, true);
UPDATE preview_requests SET approved_head='', approved_repository=0, approved_generation=0
WHERE approved_head<>'' AND NOT policy_approver_holds(workspace_id, approved_by, approved_auth_version, true);
DELETE FROM invitations WHERE NOT policy_approver_holds(workspace_id, issuer_id, issuer_auth_version, true);
UPDATE servers SET enrollment_hash=NULL, enrollment_expires=NULL
WHERE enrollment_hash IS NOT NULL AND NOT policy_approver_holds(workspace_id, enrollment_issuer_id, enrollment_issuer_auth_version, true);
-- The earlier release lets an automation credential run any target whose approver is an owner, so
-- a target whose owner's approval was withdrawn becomes runnable on demand again if that approver
-- is an owner now. It has no column to keep that from happening; review build targets after a
-- downgrade.
DROP INDEX preview_requests_cleanup_actor;
DROP INDEX preview_requests_approved_by;
ALTER TABLE preview_requests DROP COLUMN approved_auth_version, DROP COLUMN approved_by;
DROP INDEX build_targets_policy_actor;
DROP INDEX git_sources_policy_actor;
ALTER TABLE pipeline_auto_deployments DROP COLUMN actor_auth_version;
ALTER TABLE build_targets DROP COLUMN policy_owner, DROP COLUMN policy_auth_version;
ALTER TABLE git_sources DROP COLUMN policy_auth_version;
DROP INDEX automation_credentials_creator;
ALTER TABLE servers DROP CONSTRAINT servers_enrollment_issuer,
    DROP COLUMN enrollment_issuer_auth_version, DROP COLUMN enrollment_issuer_id;
DROP INDEX invitations_issuer;
ALTER TABLE invitations DROP COLUMN issuer_auth_version, DROP COLUMN issuer_id;
DROP FUNCTION policy_approver_holds(uuid, uuid, bigint, boolean);
