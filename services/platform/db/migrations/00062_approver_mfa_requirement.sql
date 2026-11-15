-- +goose Up
-- astilba: not additive: it replaces policy_approver_holds, so an older release would stop the approvals of members without two-factor authentication while it still shows them as current.

-- Whether account meets the two-factor requirement of workspace ws: the workspace doesn't require
-- it, or the account has set it up. Every standing approval and deferred action applies the
-- requirement through this, as a member's own writes do at admission.
CREATE FUNCTION account_meets_workspace_mfa(ws uuid, account uuid) RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE
RETURN EXISTS (
    SELECT 1 FROM workspaces w, accounts a
    WHERE w.id=ws AND a.id=account AND (NOT w.require_mfa OR a.totp_secret IS NOT NULL)
);

-- A standing approval or deferred action acts on its approver's say-so, so it holds only while
-- they could still give it: they are a member with the role it needs, with the authentication
-- version they gave it with, and they meet the workspace's two-factor requirement. The requirement
-- is the workspace's policy rather than a judgement of the approver, so turning it off lets their
-- approvals act again. Setting up two-factor authentication changes the authentication version,
-- so it ends an approval given without it rather than resuming it: the approver gives it again.
CREATE OR REPLACE FUNCTION policy_approver_holds(ws uuid, account uuid, approved_version bigint, owner boolean) RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE
RETURN EXISTS (
    SELECT 1 FROM accounts a JOIN memberships m ON m.account_id=a.id
    WHERE a.id=account AND a.auth_version=approved_version AND m.workspace_id=ws
        AND (m.role='owner' OR (NOT owner AND m.role='operator'))
        AND account_meets_workspace_mfa(ws, account)
);

-- +goose Down
CREATE OR REPLACE FUNCTION policy_approver_holds(ws uuid, account uuid, approved_version bigint, owner boolean) RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE
RETURN EXISTS (
    SELECT 1 FROM accounts a JOIN memberships m ON m.account_id=a.id
    WHERE a.id=account AND a.auth_version=approved_version AND m.workspace_id=ws
        AND (m.role='owner' OR (NOT owner AND m.role='operator'))
);
DROP FUNCTION account_meets_workspace_mfa(uuid, uuid);
