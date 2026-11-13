-- name: InsertCatalogStaging :exec
INSERT INTO catalog_stagings(workspace_id,environment_id,id,recipe_id,recipe_version,revision_id,services,author_id)
VALUES(@workspace_id,@environment_id,@id,@recipe_id,@recipe_version,@revision_id,@services,@author_id);

-- name: ListCatalogStagings :many
-- The newest hundred. An older row's services are still in the environment only if a hundred
-- stagings since have all been removed, so the bound loses no recommendation in practice.
SELECT id,recipe_id,recipe_version,revision_id,services,created_at FROM catalog_stagings
WHERE workspace_id = @workspace_id AND environment_id = @environment_id
ORDER BY created_at DESC, id LIMIT 100;
