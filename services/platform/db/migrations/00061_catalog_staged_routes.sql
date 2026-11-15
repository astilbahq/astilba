-- +goose Up
-- The route a catalog staging created and the client allowlist it was staged with. An entry
-- whose first account becomes its administrator relies on that allowlist until the owner has
-- the account, so planning checks the route against the chosen server's container ranges while
-- it keeps exactly this allowlist. An older release leaves both empty, and its stagings are not
-- checked. Staging writes both or neither. Rows are never changed.
ALTER TABLE catalog_stagings ADD COLUMN route_id uuid;
ALTER TABLE catalog_stagings ADD COLUMN route_allow_cidrs text[];
INSERT INTO schema_additive_migrations (version) VALUES (61);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 61;
ALTER TABLE catalog_stagings DROP COLUMN route_allow_cidrs;
ALTER TABLE catalog_stagings DROP COLUMN route_id;
