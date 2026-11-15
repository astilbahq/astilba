-- +goose Up
-- astilba: not additive: it replaces policy_approver_holds, so an older release would stop the approvals of members without two-factor authentication while it still shows them as current.

-- Whether account meets the two-factor requirement of workspace ws: the workspace doesn't require
-- it, or the account has confirmed an authenticator. A setup not yet confirmed is only
-- totp_pending, so it doesn't count. Every standing approval and deferred action applies the
-- requirement through this, as a member's own writes do at admission.
CREATE FUNCTION account_meets_workspace_mfa(ws uuid, account uuid) RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE
RETURN EXISTS (
    SELECT 1 FROM workspaces w, accounts a
    WHERE w.id=ws AND a.id=account AND (NOT w.require_mfa OR a.totp_secret IS NOT NULL)
);

-- Whether account still holds the role an approval it gave with approved_version needs: an owner
-- or, unless owner is set, an operator of ws, with the authentication version it approved with.
-- This is policy_approver_holds without the two-factor requirement, which shows an owner what the
-- requirement stops, or would let act again if it were turned off.
CREATE FUNCTION approver_role_holds(ws uuid, account uuid, approved_version bigint, owner boolean) RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE
RETURN EXISTS (
    SELECT 1 FROM accounts a JOIN memberships m ON m.account_id=a.id
    WHERE a.id=account AND a.auth_version=approved_version AND m.workspace_id=ws
        AND (m.role='owner' OR (NOT owner AND m.role='operator'))
);

-- A standing approval or deferred action acts on its approver's say-so, so it holds only while
-- they could still give it: they hold the role it needs with the authentication version they gave
-- it with, and they meet the workspace's two-factor requirement. The requirement is the
-- workspace's policy rather than a judgement of the approver, so turning it off lets their
-- standing approvals act again; a deferred action that came due meanwhile was refused for good.
-- Setting up two-factor authentication changes the authentication version, so it ends an approval
-- given without it rather than resuming it: the approver gives it again.
CREATE OR REPLACE FUNCTION policy_approver_holds(ws uuid, account uuid, approved_version bigint, owner boolean) RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE
RETURN approver_role_holds(ws, account, approved_version, owner) AND account_meets_workspace_mfa(ws, account);

-- +goose Down
CREATE OR REPLACE FUNCTION policy_approver_holds(ws uuid, account uuid, approved_version bigint, owner boolean) RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE
RETURN EXISTS (
    SELECT 1 FROM accounts a JOIN memberships m ON m.account_id=a.id
    WHERE a.id=account AND a.auth_version=approved_version AND m.workspace_id=ws
        AND (m.role='owner' OR (NOT owner AND m.role='operator'))
);
DROP FUNCTION approver_role_holds(uuid, uuid, bigint, boolean);
DROP FUNCTION account_meets_workspace_mfa(uuid, uuid);
