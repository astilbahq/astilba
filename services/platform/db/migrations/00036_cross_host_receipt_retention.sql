-- +goose Up
-- Whether any receipt still carries resource logs in the shape retention strips: an object of
-- receipts whose observation's resources are an array holding an object with logs. Other shapes,
-- which an import can carry, are left as they are, so they don't count. The body is parsed when the
-- function is created, so the index below rebuilds whatever search_path a restore uses.
CREATE FUNCTION cross_host_receipts_hold_logs(receipts jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE
RETURN EXISTS (
 SELECT 1 FROM jsonb_each(CASE WHEN jsonb_typeof(receipts)='object' THEN receipts ELSE '{}'::jsonb END) receipt
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(receipt.value->'observation'->'resources')='array' THEN receipt.value->'observation'->'resources' ELSE '[]'::jsonb END) resource
 WHERE jsonb_typeof(resource.value)='object' AND resource.value ? 'logs'
);
-- Only operations whose receipts still carry logs, so a retention pass reads those and not every
-- operation it has already cleaned. A write that brings logs back, such as a recovery merge, puts
-- the operation back.
CREATE INDEX cross_host_receipt_retention ON cross_host_operations(id) WHERE cross_host_receipts_hold_logs(receipts);

-- +goose Down
DROP INDEX cross_host_receipt_retention;
DROP FUNCTION cross_host_receipts_hold_logs(jsonb);
