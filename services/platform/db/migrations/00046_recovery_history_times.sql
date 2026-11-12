-- +goose Up
-- When an operation committed a runtime change and when it last ended. A recovery base carries a
-- window of applied history chosen by these times, so a backup taken inside the window can be
-- checked against it (docs/cross-host-placement.md).
--   runtime_recorded_at is set once, by the transaction that records the operation's runtime
--   outcome.
--   ended_at is the time the operation last became succeeded, failed or cancelled. Recovery can
--   resume a cancelled root, which clears it until the root ends again.
-- Neither can be moved by a later write. Rows from before this migration have neither; those
-- created in the last 97 days (the 90-day window and its margin) get the migration's time, which
-- is no earlier than the true one, so a window chosen from them can only start earlier. Older
-- rows fall back to their creation time.
ALTER TABLE execution_operations ADD COLUMN runtime_recorded_at timestamptz, ADD COLUMN ended_at timestamptz;
-- +goose StatementBegin
CREATE FUNCTION execution_operation_times() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' THEN
  NEW.runtime_recorded_at := COALESCE(OLD.runtime_recorded_at, NEW.runtime_recorded_at);
  IF OLD.state IN ('succeeded','failed','cancelled') AND NEW.state IN ('succeeded','failed','cancelled') THEN
   NEW.ended_at := COALESCE(OLD.ended_at, NEW.ended_at);
  END IF;
 END IF;
 IF NEW.state NOT IN ('succeeded','failed','cancelled') THEN
  NEW.ended_at := NULL;
 ELSIF NEW.ended_at IS NULL THEN
  NEW.ended_at := clock_timestamp();
 END IF;
 IF NEW.runtime_outcome IS NULL AND NEW.runtime_recorded_at IS NOT NULL THEN
  RAISE EXCEPTION 'only a recorded runtime outcome has a recording time';
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
UPDATE execution_operations SET runtime_recorded_at = now() WHERE runtime_outcome IS NOT NULL AND created_at > now() - interval '97 days';
UPDATE execution_operations SET ended_at = now() WHERE state IN ('succeeded','failed','cancelled') AND created_at > now() - interval '97 days';
CREATE TRIGGER execution_operation_times BEFORE INSERT OR UPDATE ON execution_operations
 FOR EACH ROW EXECUTE FUNCTION execution_operation_times();
-- An environment's applied history in generation order, which a recovery base's ledger reads.
CREATE INDEX execution_operations_applied_history ON execution_operations(workspace_id, environment_id, generation)
 WHERE runtime_outcome IS NOT NULL;
-- Roots and runtime changes by when they ended, which a recovery base's settled list reads.
CREATE INDEX execution_operations_settled ON execution_operations(workspace_id, ended_at)
 WHERE parent_id IS NULL AND (kind = 'crosshost' OR runtime_outcome IS NOT NULL);

-- Every recovery base this management created or a recovery took over. A later base names the
-- ones a host may still hold, which vouches that they are its ancestors: an agent keeps a base
-- until its primary closes the root, and then the two latest closed per environment. closed_at is
-- when the primary acknowledged a closing phase (complete, abort or checkpoint-closed).
-- captured_at is the base's capture time; bases from before this migration take their root's
-- creation, which is no later.
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
SELECT c.workspace_id,c.id,c.parent_id,p.environment_id,p.server_id,p.created_at,
 (SELECT min(COALESCE(k.ended_at,k.created_at)) FROM execution_operations k
  WHERE k.workspace_id=c.workspace_id AND k.parent_id=c.parent_id AND k.server_id=p.server_id AND k.kind='crosshost'
   AND k.state='succeeded' AND k.command->'crosshost'->>'phase' IN ('complete','abort','checkpoint-closed'))
FROM cross_host_checkpoints c JOIN execution_operations p ON p.workspace_id=c.workspace_id AND p.id=c.parent_id
WHERE c.sequence=0 AND p.environment_id IS NOT NULL;

-- +goose Down
DROP TABLE recovery_bases;
DROP INDEX execution_operations_settled;
DROP INDEX execution_operations_applied_history;
DROP TRIGGER execution_operation_times ON execution_operations;
DROP FUNCTION execution_operation_times();
ALTER TABLE execution_operations DROP COLUMN ended_at, DROP COLUMN runtime_recorded_at;
