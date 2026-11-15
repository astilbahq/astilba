-- name: InsertCatalogStaging :exec
INSERT INTO catalog_stagings(workspace_id,environment_id,id,recipe_id,recipe_version,revision_id,services,author_id,route_id,route_allow_cidrs)
VALUES(@workspace_id,@environment_id,@id,@recipe_id,@recipe_version,@revision_id,@services,@author_id,@route_id,@route_allow_cidrs);

-- name: ListCatalogStagings :many
-- The newest hundred. An older row's services are still in the environment only if a hundred
-- stagings since have all been removed, so the bound loses no recommendation in practice.
SELECT id,recipe_id,recipe_version,revision_id,services,created_at FROM catalog_stagings
WHERE workspace_id = @workspace_id AND environment_id = @environment_id
ORDER BY created_at DESC, id LIMIT 100;

-- name: ListCatalogStagedRoutes :many
-- The routes catalog stagings created in the environment, with the allowlist each was staged
-- with. A route ID is new at each staging, so a route has at most one row.
SELECT route_id::uuid AS route_id, route_allow_cidrs::text[] AS route_allow_cidrs FROM catalog_stagings
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND route_id IS NOT NULL;
