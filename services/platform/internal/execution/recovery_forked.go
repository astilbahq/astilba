package execution

import (
	"encoding/json/v2"
	"maps"
	"slices"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// forkedRoot is what recovery takes from the base of a root a management N's doesn't descend from
// started: the rows the root needs that N lacks, and the environments among them.
type forkedRoot struct {
	Tables       map[string][]portability.Row
	Environments map[uuid.UUID]bool
}

// environmentScoped are the tables whose rows belong to one environment by environment_id, as an
// export carries them with it (archiveFilters).
var environmentScoped = []string{"deployment_units", "environment_layouts", "secrets", "managed_volumes", "managed_resources", "route_claims", "visual_runtime", "cross_host_runtime", "git_sources", "build_targets", "backup_policies", "backup_cutovers", "network_endpoints", "specification_revisions"}

// forkedRootRows closes over what root names in its base f, and keeps the rows the newest base
// lacks: the root and its children, their cross-host rows, the plans, requests and revisions they
// name, and each environment they run in that the newest base doesn't carry, with that
// environment's own rows. Nothing else of f is taken, so f's older copies of rows the newest base
// left out (a retired preview, an ended operation, a deleted secret) stay out. What the root
// names outside this closure is in the newest base or the restored database, or the merge refuses.
func forkedRootRows(f portability.Archive, root uuid.UUID, newest map[string]map[string]bool, newestEnvironments map[uuid.UUID]bool) (forkedRoot, error) {
	kinds := map[string]map[uuid.UUID]bool{}
	for _, kind := range []string{"op", "request", "plan", "revision", "env", "endpoint", "secret"} {
		kinds[kind] = map[uuid.UUID]bool{}
	}
	kinds["op"][root] = true
	// A rule says which kind an included row joins as, by which column, and which kinds it names.
	type edge struct {
		column, kind string
	}
	type rule struct {
		table   string
		include []edge
		names   []edge
	}
	rules := []rule{
		{"execution_operations", []edge{{"id", "op"}, {"parent_id", "op"}}, []edge{{"id", "op"}, {"visual_plan_id", "request"}, {"runtime_plan_id", "request"}, {"plan_id", "plan"}, {"environment_id", "env"}}},
		{"cross_host_operations", []edge{{"id", "op"}}, []edge{{"plan_id", "request"}}},
		{"cross_host_approvals", []edge{{"parent_id", "op"}}, nil},
		{"cross_host_reservations", []edge{{"parent_id", "op"}}, nil},
		{"visual_requests", []edge{{"id", "request"}, {"operation_id", "op"}}, []edge{{"id", "request"}, {"revision_id", "revision"}, {"environment_id", "env"}}},
		{"visual_plans", []edge{{"id", "request"}}, nil},
		{"network_plan_endpoints", []edge{{"plan_id", "request"}}, []edge{{"endpoint_id", "endpoint"}}},
		{"network_plan_sources", []edge{{"plan_id", "request"}}, []edge{{"endpoint_id", "endpoint"}}},
		{"execution_plans", []edge{{"id", "plan"}}, []edge{{"revision_id", "revision"}}},
		{"specification_revisions", []edge{{"id", "revision"}}, nil},
		{"environments", []edge{{"id", "env"}}, []edge{{"draft_revision_id", "revision"}, {"applied_revision_id", "revision"}, {"runtime_operation_id", "op"}, {"retirement_operation_id", "op"}}},
		{"network_endpoints", []edge{{"id", "endpoint"}}, []edge{{"id", "endpoint"}}},
		{"network_endpoint_sources", []edge{{"endpoint_id", "endpoint"}}, nil},
		{"secrets", nil, []edge{{"id", "secret"}}},
		{"secret_versions", []edge{{"secret_id", "secret"}}, nil},
	}
	included := map[string]map[int]bool{}
	for changed := true; changed; {
		changed = false
		// Only an environment the newest base doesn't carry brings its own rows.
		forked := map[uuid.UUID]bool{}
		for id := range kinds["env"] {
			if !newestEnvironments[id] {
				forked[id] = true
			}
		}
		for _, r := range rules {
			for i, row := range f.Tables[r.table] {
				if included[r.table][i] {
					continue
				}
				in := false
				for _, e := range r.include {
					id, err := recoveryOptionalID(row, e.column)
					if err != nil {
						return forkedRoot{}, err
					}
					if id != uuid.Nil && (kinds[e.kind][id] && (r.table != "environments" || forked[id])) {
						in = true
					}
				}
				if !in && slices.Contains(environmentScoped, r.table) {
					id, err := recoveryOptionalID(row, "environment_id")
					if err != nil {
						return forkedRoot{}, err
					}
					in = forked[id]
				}
				if !in {
					continue
				}
				if included[r.table] == nil {
					included[r.table] = map[int]bool{}
				}
				included[r.table][i], changed = true, true
				for _, e := range r.names {
					id, err := recoveryOptionalID(row, e.column)
					if err != nil {
						return forkedRoot{}, err
					}
					if id != uuid.Nil {
						kinds[e.kind][id] = true
					}
				}
			}
		}
		// Environment-scoped tables without a rule above are matched by environment only.
		for _, table := range environmentScoped {
			if slices.ContainsFunc(rules, func(r rule) bool { return r.table == table }) {
				continue
			}
			for i, row := range f.Tables[table] {
				if included[table][i] {
					continue
				}
				id, err := recoveryOptionalID(row, "environment_id")
				if err != nil {
					return forkedRoot{}, err
				}
				if forked[id] {
					if included[table] == nil {
						included[table] = map[int]bool{}
					}
					included[table][i], changed = true, true
				}
			}
		}
	}
	out := forkedRoot{Tables: map[string][]portability.Row{}, Environments: map[uuid.UUID]bool{}}
	for table, rows := range included {
		for _, i := range slices.Sorted(maps.Keys(rows)) {
			row := f.Tables[table][i]
			key, err := portability.RecoveryRowKey(table, row)
			if err != nil {
				return forkedRoot{}, err
			}
			if newest[table][key] {
				continue
			}
			out.Tables[table] = append(out.Tables[table], row)
			if table == "environments" {
				id, _ := recoveryOptionalID(row, "id")
				out.Environments[id] = true
			}
		}
	}
	return out, nil
}

// recoveryOptionalID reads a nullable id column, or uuid.Nil when the row has none.
func recoveryOptionalID(row portability.Row, column string) (uuid.UUID, error) {
	raw, ok := row[column]
	if !ok {
		return uuid.Nil, nil
	}
	var id *uuid.UUID
	if json.Unmarshal(raw, &id) != nil {
		return uuid.Nil, portability.ErrArchive
	}
	if id == nil {
		return uuid.Nil, nil
	}
	return *id, nil
}
