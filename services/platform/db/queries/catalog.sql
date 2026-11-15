-- name: InsertCatalogStaging :exec
INSERT INTO catalog_stagings(workspace_id,environment_id,id,recipe_id,recipe_version,revision_id,services,author_id,staged_routes)
VALUES(@workspace_id,@environment_id,@id,@recipe_id,@recipe_version,@revision_id,@services,@author_id,@staged_routes);

-- name: ListCatalogStagings :many
-- The newest hundred. An older row's services are still in the environment only if a hundred
-- stagings since have all been removed, so the bound loses no recommendation in practice.
SELECT id,recipe_id,recipe_version,revision_id,services,created_at FROM catalog_stagings
WHERE workspace_id = @workspace_id AND environment_id = @environment_id
ORDER BY created_at DESC, id LIMIT 100;

-- name: ListCatalogStagingProvenance :many
-- Every staging in the environment with the routes it records as staged. A staging from before
-- that record also brings the specification it saved, from which planning works them out; its
-- revision always exists while the staging does, and the outer join keeps one whose revision can't
-- be found, which planning then reviews.
SELECT s.recipe_id, s.recipe_version, s.services, s.staged_routes, r.specification FROM catalog_stagings s
LEFT JOIN specification_revisions r ON s.staged_routes IS NULL AND r.workspace_id = s.workspace_id AND r.environment_id = s.environment_id AND r.id = s.revision_id
WHERE s.workspace_id = @workspace_id AND s.environment_id = @environment_id
ORDER BY s.created_at, s.id;
