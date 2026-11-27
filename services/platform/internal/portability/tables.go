package portability

import "slices"

// These literals are the archive format, not database introspection. New schema
// columns are excluded until their import and credential semantics are reviewed.
type table struct{ Name, Columns string }

var legacyTables = []table{
	{Name: "projects", Columns: "workspace_id,id,name,created_at"},
	{Name: "servers", Columns: "id,workspace_id,name,role,machine_id,revoked,rotation_requested,capabilities,preflight,last_seen,update_generation,ingress_ports,controller_generation,awaiting_transfer"},
	{Name: "environments", Columns: "workspace_id,project_id,id,name,configuration_owner,draft_revision_id,created_at,execution_generation,applied_revision_id"},
	{Name: "deployment_units", Columns: "workspace_id,id,environment_id,runtime,server_id"},
	{Name: "specification_revisions", Columns: "workspace_id,environment_id,id,author_id,specification,content_hash,created_at"},
	{Name: "environment_layouts", Columns: "workspace_id,environment_id,positions,updated_at"},
	{Name: "secrets", Columns: "workspace_id,environment_id,id,name"},
	{Name: "secret_versions", Columns: "workspace_id,secret_id,id,encrypted_value,author_id,created_at"},
	{Name: "execution_plans", Columns: "id,workspace_id,server_id,environment_id,revision_id,action,resource_id,image,hash,expires_at"},
	{Name: "execution_operations", Columns: "id,workspace_id,server_id,environment_id,generation,plan_id,request_key,command,kind,state,cancel_requested,report_sequence,steps,note,created_at,visual_plan_id"},
	{Name: "visual_requests", Columns: "id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate"},
	{Name: "visual_plans", Columns: "id,workspace_id,environment_id,plan"},
	{Name: "visual_runtime", Columns: "workspace_id,environment_id,unit_id,server_id,plan_id,observation,observed_at"},
	{Name: "managed_volumes", Columns: "workspace_id,environment_id,unit_id,id,deleted,postgres_init_hash"},
	{Name: "managed_resources", Columns: "workspace_id,environment_id,unit_id,id"},
	{Name: "route_claims", Columns: "server_id,claim,workspace_id,environment_id"},
	{Name: "git_connections", Columns: "id,workspace_id,configuration,encrypted_access"},
	{Name: "registries", Columns: "id,workspace_id,configuration,encrypted_access"},
	{Name: "build_secret_versions", Columns: "id,workspace_id,name,encrypted_value,created_at"},
	{Name: "git_sources", Columns: "workspace_id,environment_id,connection_id,configuration,imported_revision_id,imported_commit,pending_commit,status,policy_actor"},
	{Name: "build_targets", Columns: "workspace_id,environment_id,application_id,connection_id,configuration,input_hash,requested_commit,policy_actor"},
	{Name: "pipeline_runs", Columns: "id,workspace_id,environment_id,application_id,revision_id,configuration,result,artifact_id,created_at"},
	{Name: "build_artifacts", Columns: "id,workspace_id,environment_id,identity,created_at"},
	{Name: "git_submissions", Columns: "workspace_id,environment_id,revision_id,operation_id,request_key"},
	{Name: "pipeline_auto_deployments", Columns: "id,workspace_id,environment_id,revision_id,actor_id,plan_id,operation_id,state,note"},
	{Name: "backup_destinations", Columns: "id,workspace_id,configuration,encrypted_access"},
	{Name: "backup_policies", Columns: "id,workspace_id,environment_id,server_id,configuration,encrypted_access,status,status_sequence"},
	{Name: "supported_upgrades", Columns: "id,author_id,workspace_id,environment_id,server_id,plan,status"},
	{Name: "audit_events", Columns: "workspace_id,id,actor_id,action,resource_id,created_at"},
}

// Version 2 preserves preview purpose and cleanup ownership. Version 1 remains a
// fixed catalog so archives made before previews can still be decoded strictly.
var version5Tables = func() []table {
	out := append([]table{}, legacyTables...)
	for i := range out {
		switch out[i].Name {
		case "servers":
			out[i].Columns += ",preview_only"
		case "environments":
			out[i].Columns += ",purpose"
		case "deployment_units":
			out[i].Columns += ",primary_unit"
		case "execution_operations":
			out[i].Columns += ",parent_id"
		}
	}
	return append(out,
		table{Name: "backup_cutovers", Columns: "workspace_id,environment_id,revision_id,policy_id,source_plan_id,configuration"},
		table{Name: "workspace_networks", Columns: "id,workspace_id,prefix,generation"},
		table{Name: "network_members", Columns: "workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,enabled,approved_controller,generation,acknowledged_generation,manifest_hash,installed_manifest,note"},
		table{Name: "network_endpoints", Columns: "id,workspace_id,environment_id,server_id,unit_id,resource_id,instance_id,listen_port,container_port,retired,plan_id"},
		table{Name: "network_endpoint_sources", Columns: "workspace_id,endpoint_id,server_id,unit_id,active,ingress,plan_id"},
		table{Name: "network_plan_endpoints", Columns: "plan_id,workspace_id,endpoint_id"},
		table{Name: "network_plan_sources", Columns: "plan_id,workspace_id,endpoint_id,server_id,unit_id,ingress"},
		table{Name: "cross_host_operations", Columns: "id,workspace_id,environment_id,plan_id,phase,receipts"},
		table{Name: "cross_host_approvals", Columns: "workspace_id,request_key,parent_id,upgrade_id,action,expected_phase"},
		table{Name: "cross_host_runtime", Columns: "workspace_id,environment_id,unit_id,server_id,plan_id,inventory,observed_at,cpu_millis,memory_mib"},
		table{Name: "cross_host_reservations", Columns: "server_id,workspace_id,parent_id,cpu_millis,memory_mib"},
		table{Name: "preview_policies", Columns: "id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,generation,enabled,configuration,discovery_page,next_discovery_at,note"},
		table{Name: "preview_requests", Columns: "id,workspace_id,policy_id,number,head,head_repository,fork,approved_head,approved_repository,approved_generation,generation,environment_id,application_id,policy_generation,configuration,state,build_id,plan_id,operation_id,built_head,closed_at,cleanup_at,cleanup_requested,resume_after_cancel,cleanup_actor,cleanup_auth_version,cleanup_controller,next_check_at,note,created_at,updated_at"})
}()

// ArchiveVersion is the format exports write, and the tables recovery bases carry
// (RecoveryArchiveVersion). Version 7 added source_submissions; version 8 added each environment's
// retirement and the PostgreSQL major each managed volume was initialised by; version 9 added
// purges, retired secrets and erased secret values; version 11 added which preview requests
// revoking their preview's host released; version 13 added when each server's revocation began and
// when management first received each backup snapshot; version 15 added restores of a revoked
// server's backup snapshots onto another server; version 17 added each restore's latest discard;
// version 19 added the server each backup destination is bound to; version 21 added managed backup
// storage accounts and the keys management minted with them. Versions 10, 12, 14, 16, 18 and 20 are
// recovery bases.
const ArchiveVersion = 21

var version7Tables = func() []table {
	out := append([]table{}, version5Tables...)
	out = append(out, table{Name: "source_submissions", Columns: "id,workspace_id,environment_id,revision_id,request_key,actor_id,configuration,state,note,result,created_at,finished_at"})
	for i := range out {
		switch out[i].Name {
		case "environments":
			out[i].Columns += ",runtime_operation_id"
		case "execution_operations":
			out[i].Columns += ",runtime_outcome,runtime_plan_id"
		}
	}
	return out
}()

// Version 8 carries each environment's retirement and the PostgreSQL major each managed volume
// was initialised by. docs/environment-retirement.md says how an import treats them.
var version8Tables = func() []table {
	out := append([]table{}, version7Tables...)
	for i := range out {
		switch out[i].Name {
		case "environments":
			out[i].Columns += ",state,retired_at,retired_by,retirement_operation_id,retired_without_host"
		case "managed_volumes":
			out[i].Columns += ",postgres_major"
		}
	}
	return out
}()

// Version 9 carries each environment's purge, each secret's retirement and which secret versions
// were erased. An erased version has no value to carry, and imports erased.
var version9Tables = func() []table {
	out := append([]table{}, version8Tables...)
	for i := range out {
		switch out[i].Name {
		case "environments":
			out[i].Columns += ",purged_at,purged_by,purge_operation_id,purged_without_host"
		case "secrets":
			out[i].Columns += ",retired_at"
		case "secret_versions":
			out[i].Columns += ",erased_at"
		}
	}
	return out
}()

// Version 11 carries whether revoking its preview's host released a preview request, which its
// note tells the owner while the request has a new preview elsewhere.
var version11Tables = func() []table {
	out := append([]table{}, version9Tables...)
	for i := range out {
		if out[i].Name == "preview_requests" {
			out[i].Columns += ",released_by_revocation"
		}
	}
	return out
}()

// Version 13 carries when each server's revocation began and when management first received each
// backup snapshot, which restoring onto a replacement server shows the owner. Version 12 is a
// recovery base.
var version13Tables = func() []table {
	out := append([]table{}, version11Tables...)
	for i := range out {
		switch out[i].Name {
		case "servers":
			out[i].Columns += ",revoked_at"
		case "backup_policies":
			out[i].Columns += ",snapshot_receipts"
		}
	}
	return out
}()

// Version 15 carries each restore of a revoked server's backup snapshot onto another server, which
// a cutover is reviewed against. Version 14 is a recovery base.
var version15Tables = append(append([]table{}, version13Tables...), table{Name: "backup_recoveries", Columns: "id,workspace_id,environment_id,policy_id,operation_id,source_server_id,server_id,plan_id,snapshot,first_reported_at,target_volume_id,created_at,result"})

// Version 17 carries each restore's latest discard of its volume, which holds the volume while it
// is active. Version 16 is a recovery base.
var version17Tables = func() []table {
	out := append([]table{}, version15Tables...)
	for i := range out {
		if out[i].Name == "backup_recoveries" {
			out[i].Columns += ",discard_operation_id"
		}
	}
	return out
}()

// Version 19 carries the server each S3 backup destination is bound to, and whether it is shared.
// Version 18 is a recovery base.
var version19Tables = func() []table {
	out := append([]table{}, version17Tables...)
	for i := range out {
		if out[i].Name == "backup_destinations" {
			out[i].Columns += ",server_id,shared"
		}
	}
	return out
}()

// Version 21 carries managed backup storage accounts, with their admin credentials, and the keys
// management minted with them, with each server's revocation count their handovers are bound to.
// An account comes before the destinations that name it. Version 20 is a recovery base.
var tables = func() []table {
	out := append([]table{}, version19Tables...)
	for i := range out {
		switch out[i].Name {
		case "servers":
			out[i].Columns += ",revocations"
		case "backup_destinations":
			out[i].Columns += ",account_id"
		}
	}
	i := slices.IndexFunc(out, func(t table) bool { return t.Name == "backup_destinations" })
	out = slices.Insert(out, i, table{Name: "backup_storage_accounts", Columns: "id,workspace_id,name,aws_account,admin_arn,boundary,encrypted_credential,generation,peers,created_at"})
	return append(out, table{Name: "backup_storage_keys", Columns: "id,workspace_id,account_id,destination_id,holder_server_id,purpose,recovery_request,recovery_policy_id,recovery_snapshot,recovery_id,scope,user_name,access_key_id,state,mint_deadline,handed_over,activated_at,revoked_at,installs,found_tags,encrypted_secret,created_at"})
}()
