-- +goose Up
ALTER TABLE hosted_subscriptions ADD COLUMN notice_checked_at timestamptz NOT NULL DEFAULT '-infinity';

-- +goose Down
ALTER TABLE hosted_subscriptions DROP COLUMN notice_checked_at;
