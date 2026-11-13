-- +goose Up
-- astilba: not additive: adds a trigger to execution_operations, which an older release writes, and backfills its new columns.
-- When an operation committed a runtime change and when it ended, in this database. A recovery
-- base carries a window of applied history chosen by these times, so a backup taken inside the
-- window can be checked against it (docs/cross-host-placement.md).
--   runtime_recorded_at is set by the transaction that records the operation's runtime outcome.
--   An outcome that arrives with its row, from an import or a recovery, has none.
--   ended_at is when the operation last went from unfinished to succeeded, failed or cancelled
--   here, or arrived ended, from an import or a recovery, which is no earlier than it really
--   ended. Recovery can resume a cancelled root, which clears it until the root ends again.
-- Once set, neither is moved by a later write. A row without one falls back to its creation time.
-- Rows created in the last 97 days (the 90-day window and its margin) take the migration's time,
-- which is no earlier than the true one, so a window chosen from them can only start earlier.
ALTER TABLE execution_operations ADD COLUMN runtime_recorded_at timestamptz, ADD COLUMN ended_at timestamptz;
-- +goose StatementBegin
CREATE FUNCTION execution_operation_times() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'INSERT' THEN
  NEW.ended_at := CASE WHEN NEW.state IN ('succeeded','failed','cancelled') THEN clock_timestamp() END;
 ELSE
  NEW.runtime_recorded_at := COALESCE(OLD.runtime_recorded_at, NEW.runtime_recorded_at);
  IF NEW.state NOT IN ('succeeded','failed','cancelled') THEN
   NEW.ended_at := NULL;
  ELSIF OLD.state IN ('succeeded','failed','cancelled') THEN
   NEW.ended_at := OLD.ended_at;
  ELSE
   NEW.ended_at := clock_timestamp();
  END IF;
 END IF;
 IF NEW.runtime_outcome IS NULL AND NEW.runtime_recorded_at IS NOT NULL THEN
  RAISE EXCEPTION 'only a recorded runtime outcome has a recording time';
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
UPDATE execution_operations SET
 runtime_recorded_at = CASE WHEN runtime_outcome IS NOT NULL THEN now() END,
 ended_at = CASE WHEN state IN ('succeeded','failed','cancelled') THEN now() END
WHERE created_at > now() - interval '97 days' AND (runtime_outcome IS NOT NULL OR state IN ('succeeded','failed','cancelled'));
CREATE TRIGGER execution_operation_times BEFORE INSERT OR UPDATE ON execution_operations
 FOR EACH ROW EXECUTE FUNCTION execution_operation_times();
-- An environment's applied history in generation order, which a recovery base's ledger reads.
CREATE INDEX execution_operations_applied_history ON execution_operations(workspace_id, environment_id, generation)
 WHERE runtime_outcome IS NOT NULL;
-- Roots and runtime changes by when they ended, which a recovery base's settled list reads.
CREATE INDEX execution_operations_settled ON execution_operations(workspace_id, ended_at)
 WHERE parent_id IS NULL AND (kind = 'crosshost' OR runtime_outcome IS NOT NULL);

-- Every recovery base this management captured or a recovery took over. A later base names the
-- ones a host may still hold, which vouches that they are its ancestors: an agent keeps a base
-- until its primary closes the root, and then the two latest closed per environment. closed_at is
-- when the primary acknowledged a closing phase (complete, abort or checkpoint-closed).
-- captured_at is the base's capture time. Bases from before this migration take their root's
-- creation, which is no later, and are closed when their closing phase was created, which keeps
-- the order they closed in.
CREATE TABLE recovery_bases (
 workspace_id uuid NOT NULL REFERENCES workspaces(id),
 id uuid NOT NULL,
 parent_id uuid NOT NULL,
 environment_id uuid NOT NULL,
 primary_server_id uuid NOT NULL,
 captured_at timestamptz NOT NULL,
 closed_at timestamptz,
 PRIMARY KEY(workspace_id,id),
 UNIQUE(workspace_id,parent_id)
);
CREATE INDEX recovery_bases_closed ON recovery_bases(workspace_id,environment_id,primary_server_id,closed_at);
INSERT INTO recovery_bases(workspace_id,id,parent_id,environment_id,primary_server_id,captured_at,closed_at)
SELECT c.workspace_id,c.id,c.parent_id,p.environment_id,p.server_id,p.created_at,k.closed_at
FROM cross_host_checkpoints c
JOIN execution_operations p ON p.workspace_id=c.workspace_id AND p.id=c.parent_id
LEFT JOIN (SELECT workspace_id,parent_id,server_id,min(created_at) AS closed_at FROM execution_operations
  WHERE parent_id IS NOT NULL AND kind='crosshost' AND state='succeeded' AND command->'crosshost'->>'phase' IN ('complete','abort','checkpoint-closed')
  GROUP BY workspace_id,parent_id,server_id) k ON k.workspace_id=c.workspace_id AND k.parent_id=c.parent_id AND k.server_id=p.server_id
WHERE c.sequence=0 AND p.environment_id IS NOT NULL;

-- +goose Down
DROP TABLE recovery_bases;
DROP INDEX execution_operations_settled;
DROP INDEX execution_operations_applied_history;
DROP TRIGGER execution_operation_times ON execution_operations;
DROP FUNCTION execution_operation_times();
ALTER TABLE execution_operations DROP COLUMN ended_at, DROP COLUMN runtime_recorded_at;
