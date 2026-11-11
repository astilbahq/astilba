-- +goose Up
-- Private TCP destinations builds on the runner may reach past the build network policy,
-- as canonical pipeline.EgressRule objects. Builds always reach public addresses.
ALTER TABLE servers ADD COLUMN build_egress jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(build_egress)='array' AND jsonb_array_length(build_egress)<=32);

-- +goose Down
ALTER TABLE servers DROP COLUMN build_egress;
