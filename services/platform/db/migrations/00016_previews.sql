-- +goose Up
ALTER TABLE servers ADD COLUMN preview_only boolean NOT NULL DEFAULT false;
-- +goose StatementBegin
CREATE FUNCTION immutable_preview_server() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.preview_only <> OLD.preview_only THEN RAISE EXCEPTION 'server preview reservation is immutable'; END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER server_preview_immutable BEFORE UPDATE ON servers FOR EACH ROW EXECUTE FUNCTION immutable_preview_server();
ALTER TABLE environments ADD COLUMN purpose text NOT NULL DEFAULT 'standard'
 CHECK(purpose IN ('standard','preview_template','preview'));
-- +goose StatementBegin
CREATE FUNCTION immutable_environment_purpose() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.purpose <> OLD.purpose THEN RAISE EXCEPTION 'environment purpose is immutable'; END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER environment_purpose_immutable BEFORE UPDATE ON environments
 FOR EACH ROW EXECUTE FUNCTION immutable_environment_purpose();

CREATE TABLE preview_policies (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 project_id uuid NOT NULL,
 environment_id uuid NOT NULL,
 revision_id uuid NOT NULL,
 application_id uuid NOT NULL,
 connection_id uuid NOT NULL,
 server_id uuid NOT NULL,
 actor_id uuid NOT NULL REFERENCES accounts(id),
 auth_version bigint NOT NULL,
 controller_generation bigint NOT NULL,
 generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
 enabled boolean NOT NULL DEFAULT false,
 note text NOT NULL DEFAULT '',
 configuration jsonb NOT NULL CHECK(octet_length(configuration::text)<=32768),
 discovery_page integer NOT NULL DEFAULT 1 CHECK(discovery_page BETWEEN 1 AND 10000),
 next_discovery_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,id),
 UNIQUE(workspace_id,environment_id),
 FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id,revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id),
 FOREIGN KEY(workspace_id,connection_id) REFERENCES git_connections(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id)
);
CREATE INDEX preview_discovery_due ON preview_policies(next_discovery_at,id) WHERE enabled;

CREATE TABLE preview_requests (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 policy_id uuid NOT NULL,
 number integer NOT NULL CHECK(number BETWEEN 1 AND 1000000000),
 head text NOT NULL DEFAULT '',
 head_repository bigint NOT NULL DEFAULT 0,
 fork boolean NOT NULL DEFAULT false,
 approved_head text NOT NULL DEFAULT '',
 approved_repository bigint NOT NULL DEFAULT 0,
 approved_generation bigint NOT NULL DEFAULT 0,
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),
 environment_id uuid,
 application_id uuid,
 policy_generation bigint NOT NULL DEFAULT 0,
 configuration jsonb,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('approval_required','queued','building','deploying','ready','failed','closing','deleting','deleted','blocked')),
 build_id uuid REFERENCES execution_operations(id),
 plan_id uuid REFERENCES visual_requests(id),
 operation_id uuid REFERENCES execution_operations(id),
 built_head text NOT NULL DEFAULT '',
 closed_at timestamptz,
 cleanup_at timestamptz,
 cleanup_requested boolean NOT NULL DEFAULT false,
 resume_after_cancel boolean NOT NULL DEFAULT false,
 cleanup_actor uuid REFERENCES accounts(id),
 cleanup_auth_version bigint,
 cleanup_controller bigint,
 next_check_at timestamptz NOT NULL DEFAULT now(),
 note text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,id),
 UNIQUE(policy_id,number),
 FOREIGN KEY(workspace_id,policy_id) REFERENCES preview_policies(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id)
);
CREATE INDEX preview_reconcile_due ON preview_requests(next_check_at,id);
-- +goose Down
DROP TRIGGER server_preview_immutable ON servers;
DROP FUNCTION immutable_preview_server;
ALTER TABLE servers DROP COLUMN preview_only;
DROP TABLE preview_requests;
DROP TABLE preview_policies;
DROP TRIGGER environment_purpose_immutable ON environments;
DROP FUNCTION immutable_environment_purpose;
ALTER TABLE environments DROP COLUMN purpose;
