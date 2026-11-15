-- +goose Up
-- Each instance reports the custody stage its release completes, so converting values to workspace
-- keys records its stage only while no instance seen lately runs a release that seals under the
-- installation keyring. NULL is a release from before instances reported it, which may.
--
-- Additive: the previous release does not read it, and its rows stay NULL.
ALTER TABLE custody_instances ADD COLUMN stage integer;

-- Whether a sealed value is in a v2 envelope, under a workspace or installation key rather than the
-- installation keyring. Every envelope is encoded with its version first and no space, so the
-- prefix is exact, and reading it never fails on a value it cannot parse: such a value counts as
-- not converted, and converting it names it.
-- +goose StatementBegin
CREATE FUNCTION custody_value_converted(value bytea) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE SET search_path = public, pg_temp AS $$
    SELECT substring(value FROM 1 FOR 13) = '\x7b2276657273696f6e223a322c'::bytea
$$;
-- +goose StatementEnd
INSERT INTO schema_additive_migrations (version) VALUES (62);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 62;
DROP FUNCTION custody_value_converted(bytea);
ALTER TABLE custody_instances DROP COLUMN stage;
