-- name: ListProjects :many
SELECT * FROM projects WHERE workspace_id=$1 ORDER BY created_at,id;
-- name: InsertProject :one
INSERT INTO projects(workspace_id,id,name) VALUES($1,$2,$3) RETURNING *;
-- name: GetProject :one
SELECT * FROM projects WHERE workspace_id=$1 AND id=$2;
-- name: ListEnvironments :many
SELECT * FROM environments WHERE workspace_id=$1 AND project_id=$2 ORDER BY created_at,id;
-- name: InsertEnvironment :one
INSERT INTO environments(workspace_id,project_id,id,name,draft_revision_id,configuration_owner,purpose) VALUES($1,$2,$3,$4,$5,COALESCE(NULLIF(sqlc.arg(configuration_owner)::text,''),'dashboard'),COALESCE(NULLIF(sqlc.arg(purpose)::text,''),'standard')) RETURNING *;
-- name: GetEnvironment :one
SELECT e.*,d.id AS unit_id,d.runtime,d.server_id FROM environments e JOIN deployment_units d
ON d.workspace_id=e.workspace_id AND d.environment_id=e.id AND d.primary_unit WHERE e.workspace_id=$1 AND e.project_id=$2 AND e.id=$3;
-- name: InsertDeploymentUnit :exec
INSERT INTO deployment_units(workspace_id,id,environment_id) VALUES($1,$2,$3);
-- name: GetRevision :one
SELECT * FROM specification_revisions WHERE workspace_id=$1 AND environment_id=$2 AND id=$3;
-- name: ListRevisions :many
SELECT id,author_id,content_hash,created_at FROM specification_revisions WHERE workspace_id=$1 AND environment_id=$2 ORDER BY created_at DESC,id DESC LIMIT 50;
-- name: InsertRevision :one
INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash) VALUES($1,$2,$3,$4,$5,$6) RETURNING *;
-- name: AdvanceDraft :one
UPDATE environments SET draft_revision_id=$4 WHERE workspace_id=$1 AND id=$2 AND draft_revision_id=$3 RETURNING id;
-- name: GetLayout :one
SELECT * FROM environment_layouts WHERE workspace_id=$1 AND environment_id=$2;
-- name: PutLayout :one
INSERT INTO environment_layouts(workspace_id,environment_id,positions) VALUES($1,$2,$3)
ON CONFLICT(workspace_id,environment_id) DO UPDATE SET positions=EXCLUDED.positions,updated_at=now() RETURNING *;
-- name: ListSecrets :many
SELECT s.id,s.name,v.id AS version_id,v.created_at FROM secrets s JOIN secret_versions v
ON v.workspace_id=s.workspace_id AND v.secret_id=s.id WHERE s.workspace_id=$1 AND s.environment_id=$2 ORDER BY s.name,v.created_at DESC;
-- name: GetSecret :one
SELECT * FROM secrets WHERE workspace_id=$1 AND environment_id=$2 AND id=$3;
-- name: InsertSecret :exec
INSERT INTO secrets(workspace_id,environment_id,id,name) VALUES($1,$2,$3,$4);
-- name: InsertSecretVersion :one
INSERT INTO secret_versions(workspace_id,secret_id,id,encrypted_value,author_id) VALUES($1,$2,$3,$4,$5) RETURNING id,created_at;
-- name: GetSecretVersion :one
SELECT v.* FROM secret_versions v JOIN secrets s ON s.workspace_id=v.workspace_id AND s.id=v.secret_id
WHERE v.workspace_id=$1 AND s.environment_id=$2 AND v.id=$3;
-- name: AllEncryptedVersions :many
SELECT workspace_id,id,encrypted_value FROM secret_versions FOR UPDATE;
-- name: RewrapSecretVersion :exec
UPDATE secret_versions SET encrypted_value=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllTOTPSecrets :many
SELECT id,totp_secret,totp_pending FROM accounts WHERE totp_secret IS NOT NULL OR totp_pending IS NOT NULL FOR UPDATE;
-- name: RewrapTOTP :exec
UPDATE accounts SET totp_secret=$2,totp_pending=$3 WHERE id=$1;
