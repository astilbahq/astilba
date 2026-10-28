-- +goose Up
CREATE TABLE workspace_networks (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL UNIQUE REFERENCES workspaces(id),
 prefix cidr NOT NULL, generation bigint NOT NULL CHECK(generation>0),
 checked_at timestamptz NOT NULL DEFAULT '-infinity',
 UNIQUE(workspace_id,id)
);
CREATE INDEX workspace_network_scan ON workspace_networks(checked_at,workspace_id);
CREATE TABLE network_members (
 workspace_id uuid NOT NULL, network_id uuid NOT NULL, server_id uuid NOT NULL,
 membership_id uuid NOT NULL UNIQUE, address inet NOT NULL, endpoint text NOT NULL,
 listen_port integer NOT NULL CHECK(listen_port BETWEEN 1024 AND 65535),
 cpu_millis integer NOT NULL CHECK(cpu_millis BETWEEN 100 AND 256000),
 memory_mib integer NOT NULL CHECK(memory_mib BETWEEN 64 AND 1048576),
 unbounded_inventory_hash text NOT NULL CHECK(unbounded_inventory_hash ~ '^[a-f0-9]{64}$'),
 public_key text NOT NULL DEFAULT '', enabled boolean NOT NULL DEFAULT true,
 approved_controller bigint NOT NULL CHECK(approved_controller>=0),
 generation bigint NOT NULL CHECK(generation>0),
 acknowledged_generation bigint NOT NULL DEFAULT 0,
 manifest_hash text NOT NULL DEFAULT '', installed_manifest jsonb, note text NOT NULL DEFAULT '',
 PRIMARY KEY(workspace_id,server_id), UNIQUE(network_id,address),
 FOREIGN KEY(workspace_id,network_id) REFERENCES workspace_networks(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id)
);
CREATE TABLE network_endpoints (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, environment_id uuid NOT NULL,
 server_id uuid NOT NULL, unit_id uuid NOT NULL, resource_id uuid NOT NULL,
 instance_id uuid NOT NULL, listen_port integer NOT NULL CHECK(listen_port BETWEEN 20000 AND 39999),
 container_port integer NOT NULL CHECK(container_port BETWEEN 1 AND 65535),
 retired boolean NOT NULL DEFAULT false, plan_id uuid,
 UNIQUE(server_id,listen_port), UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES network_members(workspace_id,server_id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id)
);
CREATE TABLE network_endpoint_sources (
 workspace_id uuid NOT NULL, endpoint_id uuid NOT NULL, server_id uuid NOT NULL, unit_id uuid NOT NULL, active boolean NOT NULL DEFAULT false, ingress boolean NOT NULL DEFAULT false, plan_id uuid,
 PRIMARY KEY(endpoint_id,server_id,unit_id),
 FOREIGN KEY(workspace_id,endpoint_id) REFERENCES network_endpoints(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES network_members(workspace_id,server_id)
);
ALTER TABLE deployment_units DROP CONSTRAINT deployment_units_workspace_id_environment_id_key;
ALTER TABLE deployment_units ADD COLUMN primary_unit boolean NOT NULL DEFAULT true;
CREATE UNIQUE INDEX primary_environment_unit ON deployment_units(workspace_id,environment_id) WHERE primary_unit;
ALTER TABLE execution_operations ADD COLUMN parent_id uuid;
ALTER TABLE execution_operations ADD CONSTRAINT execution_parent_owner UNIQUE(workspace_id,id);
ALTER TABLE execution_operations ADD CONSTRAINT execution_parent_fk FOREIGN KEY(workspace_id,parent_id) REFERENCES execution_operations(workspace_id,id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE execution_operations DROP CONSTRAINT execution_operations_kind_check;
ALTER TABLE execution_operations ADD CONSTRAINT execution_operations_kind_check CHECK(kind IN ('test','update','visual','pipeline','backup','schedule','upgrade','network','crosshost'));
DROP INDEX execution_active_environment;
CREATE UNIQUE INDEX execution_active_environment ON execution_operations(workspace_id,environment_id) WHERE parent_id IS NULL AND state NOT IN ('succeeded','failed','cancelled');
DROP INDEX execution_active_server;
CREATE UNIQUE INDEX execution_active_server ON execution_operations(server_id) WHERE kind<>'crosshost' AND NOT(kind='network' AND COALESCE(command->'network'->>'action','')='restrict') AND state NOT IN ('succeeded','failed','cancelled') AND (kind<>'pipeline' OR state<>'queued');
CREATE UNIQUE INDEX execution_active_restriction ON execution_operations(server_id) WHERE kind='network' AND command->'network'->>'action'='restrict' AND state NOT IN ('queued','succeeded','failed','cancelled');
CREATE TABLE cross_host_reservations (
 server_id uuid PRIMARY KEY, workspace_id uuid NOT NULL, parent_id uuid NOT NULL,
 cpu_millis bigint NOT NULL DEFAULT 0, memory_mib bigint NOT NULL DEFAULT 0,
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id),
 FOREIGN KEY(workspace_id,parent_id) REFERENCES execution_operations(workspace_id,id)
);
ALTER TABLE visual_requests DROP CONSTRAINT visual_requests_candidate_check;
ALTER TABLE visual_requests ADD CONSTRAINT visual_requests_candidate_check CHECK(octet_length(candidate::text)<=1048576);
ALTER TABLE visual_plans DROP CONSTRAINT visual_plans_plan_check;
ALTER TABLE visual_plans ADD CONSTRAINT visual_plans_plan_check CHECK(octet_length(plan::text)<=1048576);
CREATE TABLE cross_host_operations (
 id uuid PRIMARY KEY REFERENCES execution_operations(id), workspace_id uuid NOT NULL,
 environment_id uuid NOT NULL, plan_id uuid NOT NULL REFERENCES visual_requests(id) DEFERRABLE INITIALLY DEFERRED,
 phase text NOT NULL, receipts jsonb NOT NULL DEFAULT '{}',
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id)
);
ALTER TABLE supported_upgrades ADD CONSTRAINT supported_upgrade_owner UNIQUE(workspace_id,id);
CREATE TABLE cross_host_approvals (
 workspace_id uuid NOT NULL, request_key uuid NOT NULL, parent_id uuid NOT NULL,
 upgrade_id uuid NOT NULL, action text NOT NULL CHECK(action IN ('cutover','return','reconcile')),
 expected_phase text NOT NULL, PRIMARY KEY(workspace_id,request_key),
 FOREIGN KEY(workspace_id,parent_id) REFERENCES execution_operations(workspace_id,id),
 FOREIGN KEY(workspace_id,upgrade_id) REFERENCES supported_upgrades(workspace_id,id)
);
CREATE TABLE cross_host_runtime (
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL, unit_id uuid NOT NULL,
 server_id uuid NOT NULL, plan_id uuid NOT NULL REFERENCES visual_requests(id) DEFERRABLE INITIALLY DEFERRED,
 inventory jsonb NOT NULL, observed_at timestamptz NOT NULL DEFAULT now(),
 cpu_millis bigint NOT NULL, memory_mib bigint NOT NULL,
 PRIMARY KEY(workspace_id,unit_id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,unit_id) REFERENCES deployment_units(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id)
);

-- Recovery envelopes contain management-keyring ciphertext only. They are
-- delivered to the assigned primary and excluded from portable workspace
-- exports, which already refuse active reservations.
CREATE TABLE cross_host_checkpoints (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL,
 parent_id uuid NOT NULL, sequence bigint NOT NULL CHECK(sequence>=0),
 descriptor jsonb NOT NULL CHECK(octet_length(descriptor::text)<=8192),
 envelope bytea NOT NULL CHECK(octet_length(envelope)<=1048576),
 phase text NOT NULL DEFAULT 'base', acknowledged boolean NOT NULL DEFAULT false,
 UNIQUE(parent_id,sequence),
 FOREIGN KEY(workspace_id,parent_id) REFERENCES execution_operations(workspace_id,id)
);

CREATE TABLE cross_host_recovery_rows (
 workspace_id uuid NOT NULL REFERENCES workspaces(id), table_name text NOT NULL,
 source_hash text NOT NULL, attributed_hash text NOT NULL,
 PRIMARY KEY(workspace_id,table_name,source_hash)
);

CREATE TABLE cross_host_recoveries (
 parent_id uuid PRIMARY KEY REFERENCES cross_host_operations(id),
 workspace_id uuid NOT NULL REFERENCES workspaces(id),
 opened boolean NOT NULL DEFAULT false,
 source_hash text NOT NULL,input_hash text NOT NULL,owner_id uuid NOT NULL REFERENCES accounts(id)
);
CREATE TABLE cross_host_recovery_servers (
 parent_id uuid NOT NULL REFERENCES cross_host_recoveries(parent_id),
 workspace_id uuid NOT NULL,server_id uuid NOT NULL,
 machine_id uuid NOT NULL,controller_generation bigint NOT NULL,
 inventory jsonb NOT NULL,handover jsonb NOT NULL,verified boolean NOT NULL DEFAULT false,
 PRIMARY KEY(parent_id,server_id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id)
);

CREATE TABLE backup_cutovers (
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL, revision_id uuid PRIMARY KEY,
 policy_id uuid NOT NULL, source_plan_id uuid NOT NULL,
 configuration jsonb NOT NULL CHECK(octet_length(configuration::text)<=4096),
 FOREIGN KEY(workspace_id,environment_id,revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id),
 FOREIGN KEY(workspace_id,environment_id,policy_id) REFERENCES backup_policies(workspace_id,environment_id,id),
 FOREIGN KEY(workspace_id,environment_id,source_plan_id) REFERENCES visual_requests(workspace_id,environment_id,id)
);
CREATE TRIGGER backup_cutover_immutable BEFORE UPDATE OR DELETE ON backup_cutovers FOR EACH ROW EXECUTE FUNCTION immutable_specification();

ALTER TABLE backup_policies ADD COLUMN coordinated_checked_at timestamptz NOT NULL DEFAULT '-infinity';

CREATE TABLE network_plan_endpoints (
 plan_id uuid NOT NULL REFERENCES visual_requests(id) DEFERRABLE INITIALLY DEFERRED,
 workspace_id uuid NOT NULL, endpoint_id uuid NOT NULL,
 PRIMARY KEY(plan_id,endpoint_id),
 FOREIGN KEY(workspace_id,endpoint_id) REFERENCES network_endpoints(workspace_id,id)
);
CREATE TABLE network_plan_sources (
 plan_id uuid NOT NULL REFERENCES visual_requests(id) DEFERRABLE INITIALLY DEFERRED,
 workspace_id uuid NOT NULL, endpoint_id uuid NOT NULL, server_id uuid NOT NULL,unit_id uuid NOT NULL,ingress boolean NOT NULL DEFAULT false,
 PRIMARY KEY(plan_id,endpoint_id,server_id,unit_id),
 FOREIGN KEY(endpoint_id,server_id,unit_id) REFERENCES network_endpoint_sources(endpoint_id,server_id,unit_id),
 FOREIGN KEY(workspace_id,endpoint_id) REFERENCES network_endpoints(workspace_id,id)
);
CREATE UNIQUE INDEX secondary_server_unit ON deployment_units(workspace_id,environment_id,server_id) WHERE NOT primary_unit;

-- Network commands use a host-wide generation namespace; environment mutations
-- retain their unique environment generation across parent and child phases.
ALTER TABLE execution_operations DROP CONSTRAINT execution_operations_workspace_id_environment_id_generation_key;
CREATE UNIQUE INDEX execution_environment_generation ON execution_operations(workspace_id,environment_id,generation) WHERE kind<>'network';
