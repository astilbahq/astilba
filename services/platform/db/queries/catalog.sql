-- name: InsertCatalogStaging :exec
INSERT INTO catalog_stagings(workspace_id,environment_id,id,recipe_id,recipe_version,revision_id,services,author_id)
VALUES(@workspace_id,@environment_id,@id,@recipe_id,@recipe_version,@revision_id,@services,@author_id);

-- name: ListCatalogStagings :many
-- The newest hundred. An older row's services are still in the environment only if a hundred
-- stagings since have all been removed, so the bound loses no recommendation in practice.
SELECT id,recipe_id,recipe_version,revision_id,services,created_at FROM catalog_stagings
WHERE workspace_id = @workspace_id AND environment_id = @environment_id
ORDER BY created_at DESC, id LIMIT 100;

-- name: ListCatalogStagedRevisions :many
-- Every staging in the environment, but those of entries known to have no route, with the
-- specification it saved, from which planning works out the routes it created.
SELECT s.services, r.specification FROM catalog_stagings s
LEFT JOIN specification_revisions r ON r.workspace_id = s.workspace_id AND r.environment_id = s.environment_id AND r.id = s.revision_id
WHERE s.workspace_id = @workspace_id AND s.environment_id = @environment_id AND NOT (s.recipe_id = ANY(@routeless::text[]));

-- name: ListCatalogStagingsForClone :many
-- Every staging in the environment, for a clone to record under its own identities.
SELECT recipe_id,recipe_version,services FROM catalog_stagings
WHERE workspace_id = @workspace_id AND environment_id = @environment_id
ORDER BY created_at, id;
