-- name: AllGitAccess :many
SELECT workspace_id,id,encrypted_access FROM git_connections FOR UPDATE;
-- name: RewrapGitAccess :exec
UPDATE git_connections SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllRegistryAccess :many
SELECT workspace_id,id,encrypted_access FROM registries FOR UPDATE;
-- name: RewrapRegistryAccess :exec
UPDATE registries SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllBuildSecrets :many
SELECT workspace_id,id,encrypted_value FROM build_secret_versions FOR UPDATE;
-- name: RewrapBuildSecret :exec
UPDATE build_secret_versions SET encrypted_value=$3 WHERE workspace_id=$1 AND id=$2;
