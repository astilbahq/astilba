-- +goose Up
-- Why the network sweep holds a member back, kept apart from its note, which shows the owner the
-- most important thing about the member and can be replaced by another reason. 'unprepared' holds
-- a member whose host failed a command without a receipt while it reported itself unprepared, until
-- it reports ready. 'duplicate-key' holds a member refused for reporting another member's key. Each
-- holds until the network moves to a new generation through a revocation, a receipt or the sweep,
-- the owner saves it, or the member's next command succeeds.
ALTER TABLE network_members ADD COLUMN sweep_hold text CHECK(sweep_hold IN ('unprepared','duplicate-key'));

-- +goose Down
ALTER TABLE network_members DROP COLUMN sweep_hold;
