-- name: ConnectionsAvailable :one
-- Slots this session's role can ever hold. reserved_connections arrived in PostgreSQL 16,
-- and superusers are not subject to CONNECTION LIMIT.
SELECT LEAST(
  current_setting('max_connections')::int
    - current_setting('superuser_reserved_connections')::int
    - COALESCE(current_setting('reserved_connections', true)::int, 0),
  CASE WHEN NOT r.rolsuper THEN NULLIF(r.rolconnlimit, -1) END,
  CASE WHEN NOT r.rolsuper THEN NULLIF(d.datconnlimit, -1) END
)::int AS available
FROM pg_catalog.pg_roles r, pg_catalog.pg_database d
WHERE r.rolname = session_user AND d.datname = current_database();
