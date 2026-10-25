-- +goose Up
CREATE TABLE dns_installation (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 id uuid NOT NULL DEFAULT gen_random_uuid()
);
INSERT INTO dns_installation(singleton) VALUES(true);
CREATE TABLE server_domains (
 server_id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 base text NOT NULL,
 ipv4 text NOT NULL DEFAULT '',
 ipv6 text NOT NULL DEFAULT '',
 generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
 approved_controller bigint,
 state text NOT NULL CHECK(state IN ('pending','applied','manual','failed','review_required')),
 observed_at timestamptz,
 note text NOT NULL DEFAULT '',
 records jsonb NOT NULL DEFAULT '{}',
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id)
);
CREATE INDEX server_domains_reconcile ON server_domains(observed_at NULLS FIRST,server_id);
-- +goose Down
DROP TABLE server_domains;
DROP TABLE dns_installation;
