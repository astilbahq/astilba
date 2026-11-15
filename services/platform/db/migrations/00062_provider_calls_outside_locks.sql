-- +goose Up
-- astilba: not additive: an older release ignores the DNS lease and the Stripe tickets, so running beside one, a new DNS job's records can outlive a revoked server's withdrawal and a stale Stripe answer can restore a cancelled subscription.
-- A DNS job holds its server's lease while it asks the provider, instead of a session lock on
-- a pinned connection. A job takes the lease when it is free or has run out, and every write
-- it makes after asking the provider checks that it still holds it, so a job whose lease ran
-- out and was taken records nothing. Expiry is read from the database's clock, never a worker's.
CREATE TABLE server_dns_leases (
 server_id uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 holder uuid NOT NULL,
 expires_at timestamptz NOT NULL
);
-- Stripe is asked with nothing held, so answers about a subscription can arrive out of order.
-- Each takes a ticket before it is asked for, and the row keeps the ticket of the answer it
-- holds: an older answer about the same subscription is skipped, because the stored one was
-- asked for later and saw every change it could have.
CREATE SEQUENCE hosted_observation_tickets;
ALTER TABLE hosted_subscriptions ADD COLUMN observed_ticket bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE hosted_subscriptions DROP COLUMN observed_ticket;
DROP SEQUENCE hosted_observation_tickets;
DROP TABLE server_dns_leases;
