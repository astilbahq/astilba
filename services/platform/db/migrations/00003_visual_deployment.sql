-- +goose Up
ALTER TABLE environments ADD COLUMN applied_revision_id uuid;
ALTER TABLE environments ADD CONSTRAINT applied_revision_owner FOREIGN KEY(workspace_id,id,applied_revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id);
ALTER TABLE deployment_units ADD COLUMN server_id uuid;
ALTER TABLE deployment_units ADD CONSTRAINT unit_server_owner FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id);
ALTER TABLE servers ADD COLUMN ingress_ports jsonb NOT NULL DEFAULT '[]';
ALTER TABLE execution_operations DROP CONSTRAINT execution_operations_kind_check;
ALTER TABLE execution_operations ADD CONSTRAINT execution_operations_kind_check CHECK(kind IN ('test','update','visual'));

CREATE TABLE visual_requests (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, server_id uuid NOT NULL,
 environment_id uuid NOT NULL, unit_id uuid NOT NULL, revision_id uuid NOT NULL,
 operation_id uuid NOT NULL REFERENCES execution_operations(id),
 candidate jsonb NOT NULL CHECK(octet_length(candidate::text)<=196608),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id),
 FOREIGN KEY(workspace_id,unit_id) REFERENCES deployment_units(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id,revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id),
 UNIQUE(workspace_id,environment_id,id)
);
CREATE TRIGGER visual_request_immutable BEFORE UPDATE OR DELETE ON visual_requests FOR EACH ROW EXECUTE FUNCTION immutable_specification();
CREATE TABLE visual_plans (
 id uuid PRIMARY KEY REFERENCES visual_requests(id), workspace_id uuid NOT NULL, environment_id uuid NOT NULL,
 plan jsonb NOT NULL CHECK(octet_length(plan::text)<=196608),
 FOREIGN KEY(workspace_id,environment_id,id) REFERENCES visual_requests(workspace_id,environment_id,id)
);
CREATE TRIGGER visual_plan_immutable BEFORE UPDATE OR DELETE ON visual_plans FOR EACH ROW EXECUTE FUNCTION immutable_specification();
ALTER TABLE execution_operations ADD COLUMN visual_plan_id uuid UNIQUE REFERENCES visual_plans(id);
CREATE TABLE visual_runtime (
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL, unit_id uuid NOT NULL,
 server_id uuid NOT NULL, plan_id uuid REFERENCES visual_plans(id),
 observation jsonb NOT NULL, observed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,environment_id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,unit_id) REFERENCES deployment_units(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id)
);
CREATE TABLE managed_volumes (
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL, unit_id uuid NOT NULL, id uuid NOT NULL,
 deleted boolean NOT NULL DEFAULT false,
 postgres_init_hash text CHECK(postgres_init_hash ~ '^[a-f0-9]{64}$'),
 PRIMARY KEY(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,unit_id) REFERENCES deployment_units(workspace_id,id)
);
CREATE TABLE managed_resources (
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL, unit_id uuid NOT NULL, id uuid NOT NULL,
 PRIMARY KEY(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,unit_id) REFERENCES deployment_units(workspace_id,id)
);
CREATE TABLE route_claims (
 server_id uuid NOT NULL REFERENCES servers(id), claim text NOT NULL,
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL,
 PRIMARY KEY(server_id,claim),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id)
);
