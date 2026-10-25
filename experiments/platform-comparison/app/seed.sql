\set ON_ERROR_STOP on
BEGIN;
CREATE EXTENSION hstore;
CREATE TABLE comparison_notes (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 slug text NOT NULL UNIQUE,
 body text NOT NULL,
 attributes hstore NOT NULL DEFAULT ''::hstore,
 created_at timestamptz NOT NULL DEFAULT '2026-01-01T00:00:00Z'
);
CREATE TABLE comparison_links (
 note_id bigint PRIMARY KEY REFERENCES comparison_notes(id),
 amount numeric(12,2) NOT NULL CHECK (amount>=0)
);
INSERT INTO comparison_notes(slug,body,attributes)
 SELECT 'seed-'||i, 'fixture row '||i, hstore('ordinal',i::text)
 FROM generate_series(1,100) AS i;
INSERT INTO comparison_links(note_id,amount) SELECT id,id*1.25 FROM comparison_notes;
CREATE ROLE comparison_reader NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
GRANT SELECT ON comparison_notes,comparison_links TO comparison_reader;
COMMIT;
