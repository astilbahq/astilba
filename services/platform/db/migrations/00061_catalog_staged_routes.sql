-- +goose Up
-- The routes a catalog staging created, each with the client allowlist it was staged with, as an
-- object from route ID to allowlist, or the string "unconfirmed" for a clone's copy of a staging
-- whose routes could not be read. An entry whose first account becomes its administrator relies on
-- that allowlist until the owner has the account, so planning checks each route against the chosen
-- server's container ranges while it keeps exactly this allowlist. A clone records here which of
-- its source's routes are still staged, which the services a staging created cannot say.
--
-- Additive: the previous release makes no such check, so nothing it must obey lives only here. A
-- row it writes, before this release or while rolled back, has no value, and planning works out the
-- staged routes from the staging's saved revision instead, so no staging row needs filling in. A
-- clone the previous release makes records no stagings and keeps no link to its source, so its
-- copy of a staged route is not checked; nothing could fill that in. Rows are never changed.
ALTER TABLE catalog_stagings ADD COLUMN staged_routes jsonb;
INSERT INTO schema_additive_migrations (version) VALUES (61);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 61;
ALTER TABLE catalog_stagings DROP COLUMN staged_routes;
