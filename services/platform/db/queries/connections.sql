-- name: ConnectionsAvailable :one
-- Slots an ordinary role can ever hold. reserved_connections arrived in PostgreSQL 16.
SELECT (current_setting('max_connections')::int
  - current_setting('superuser_reserved_connections')::int
  - COALESCE(current_setting('reserved_connections', true)::int, 0))::int AS available;
