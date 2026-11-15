package portability

import "strings"

func normalizeLegacyArchive(a *Archive) error {
	if len(a.Tables) != len(legacyTables) {
		return ErrArchive
	}
	for _, t := range legacyTables {
		rows, ok := a.Tables[t.Name]
		if !ok || rows == nil {
			return ErrArchive
		}
		columns := strings.Split(t.Columns, ",")
		for _, row := range rows {
			if len(row) != len(columns) {
				return ErrArchive
			}
			for _, column := range columns {
				if _, ok := row[column]; !ok {
					return ErrArchive
				}
			}
			if t.Name == "environments" {
				setValue(row, "purpose", "standard")
			}
			if t.Name == "servers" {
				setValue(row, "preview_only", false)
			}
		}
	}
	a.Tables["preview_policies"] = []Row{}
	a.Tables["preview_requests"] = []Row{}
	a.Version = 2
	return nil
}

func normalizePreNetworkArchive(a *Archive) error {
	for _, table := range tables {
		if strings.HasPrefix(table.Name, "network_") || table.Name == "workspace_networks" || strings.HasPrefix(table.Name, "cross_host_") || table.Name == "backup_cutovers" {
			if _, ok := a.Tables[table.Name]; ok {
				return ErrArchive
			}
			a.Tables[table.Name] = []Row{}
		}
	}
	for _, row := range a.Tables["deployment_units"] {
		if _, ok := row["primary_unit"]; ok {
			return ErrArchive
		}
		setValue(row, "primary_unit", true)
	}
	for _, row := range a.Tables["execution_operations"] {
		if _, ok := row["parent_id"]; ok {
			return ErrArchive
		}
		setValue(row, "parent_id", nil)
	}
	return nil
}
