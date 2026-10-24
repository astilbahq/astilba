-- name: AllBackupDestinationAccess :many
SELECT workspace_id,id,encrypted_access FROM backup_destinations FOR UPDATE;
-- name: RewrapBackupDestinationAccess :exec
UPDATE backup_destinations SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllBackupPolicyAccess :many
SELECT workspace_id,id,encrypted_access FROM backup_policies FOR UPDATE;
-- name: RewrapBackupPolicyAccess :exec
UPDATE backup_policies SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;
