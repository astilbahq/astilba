-- +goose Up
CREATE TABLE server_metrics (
 workspace_id uuid NOT NULL,
 server_id uuid NOT NULL,
 received_at timestamptz NOT NULL,
 report jsonb NOT NULL CHECK(octet_length(report::text)<=16384),
 PRIMARY KEY(server_id,received_at),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id),
 CHECK(received_at=date_trunc('minute',received_at))
);
CREATE INDEX server_metrics_retention ON server_metrics(received_at);
