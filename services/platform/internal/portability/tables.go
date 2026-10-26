package portability

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
var tables = func() []table {
	out := append([]table{}, legacyTables...)
	for i := range out {
		switch out[i].Name {
		case "servers":
			out[i].Columns += ",preview_only"
		case "environments":
			out[i].Columns += ",purpose"
		}
	}
	return append(out,
		table{Name: "preview_policies", Columns: "id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,generation,enabled,configuration,discovery_page,next_discovery_at,note"},
		table{Name: "preview_requests", Columns: "id,workspace_id,policy_id,number,head,head_repository,fork,approved_head,approved_repository,approved_generation,generation,environment_id,application_id,policy_generation,configuration,state,build_id,plan_id,operation_id,built_head,closed_at,cleanup_at,cleanup_requested,resume_after_cancel,cleanup_actor,cleanup_auth_version,cleanup_controller,next_check_at,note,created_at,updated_at"})
}()
