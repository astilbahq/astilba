-- +goose Up
-- Set when revoking a preview's reserved host released it, and kept while the request gets a new
-- preview elsewhere, so the owner can tell it's on another host. Not exported: it only adds a
-- sentence to the request's note, and an imported or restored request simply goes without it.
ALTER TABLE preview_requests ADD COLUMN released_by_revocation boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE preview_requests DROP COLUMN released_by_revocation;
