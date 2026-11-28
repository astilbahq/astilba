package portability

import (
	"encoding/json/v2"
	"strings"

	"github.com/google/uuid"
)

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

// upgradeArchive brings an archive whose tables are version's up to this release's, refusing one
// that already carries what its version didn't have.
func upgradeArchive(archive *Archive, version int) error {
	if version == 1 {
		if err := normalizeLegacyArchive(archive); err != nil {
			return err
		}
	}
	if version < 5 {
		if err := normalizePreNetworkArchive(archive); err != nil {
			return err
		}
	}
	if version < 6 {
		for _, row := range archive.Tables["environments"] {
			if _, exists := row["runtime_operation_id"]; exists {
				return ErrArchive
			}
			setValue(row, "runtime_operation_id", nil)
		}

		for _, row := range archive.Tables["execution_operations"] {
			for _, field := range []string{"runtime_outcome", "runtime_plan_id"} {
				if _, exists := row[field]; exists {
					return ErrArchive
				}
				setValue(row, field, nil)
			}
		}
	}
	// Version 7 carries the submissions management makes. An older archive has only runners'.
	if version < 7 {
		if _, exists := archive.Tables["source_submissions"]; exists {
			return ErrArchive
		}
		archive.Tables["source_submissions"] = []Row{}
	}
	if version < 8 {
		// An environment from before retirement is active, and nothing recorded which PostgreSQL
		// major initialised a volume: its next deployment records it.
		fields := map[string]map[string]any{
			"environments":    {"state": "active", "retired_at": nil, "retired_by": nil, "retirement_operation_id": nil, "retired_without_host": false},
			"managed_volumes": {"postgres_major": nil},
		}
		for table, values := range fields {
			for _, row := range archive.Tables[table] {
				for field, value := range values {
					if _, exists := row[field]; exists {
						return ErrArchive
					}
					setValue(row, field, value)
				}
			}
		}
	}
	if version < 9 {
		// Nothing was purged, retired or erased before version 9.
		fields := map[string]map[string]any{
			"environments":    {"purged_at": nil, "purged_by": nil, "purge_operation_id": nil, "purged_without_host": false},
			"secrets":         {"retired_at": nil},
			"secret_versions": {"erased_at": nil},
		}
		for table, values := range fields {
			for _, row := range archive.Tables[table] {
				for field, value := range values {
					if _, exists := row[field]; exists {
						return ErrArchive
					}
					setValue(row, field, value)
				}
			}
		}
	}
	if version < 11 {
		// Nothing recorded which requests revocation released. Their notes go without it.
		for _, row := range archive.Tables["preview_requests"] {
			if _, exists := row["released_by_revocation"]; exists {
				return ErrArchive
			}
			setValue(row, "released_by_revocation", false)
		}
	}
	if version < 13 {
		// Nothing recorded when a revocation began or when a snapshot was first reported. A server
		// revoked then shows no time, and its snapshots stay on the manual route (docs/backups.md).
		fields := map[string]map[string]any{"servers": {"revoked_at": nil}, "backup_policies": {"snapshot_receipts": map[string]any{}}}
		for table, values := range fields {
			for _, row := range archive.Tables[table] {
				for field, value := range values {
					if _, exists := row[field]; exists {
						return ErrArchive
					}
					setValue(row, field, value)
				}
			}
		}
	}
	// Version 15 carries restores onto another server. None was made before it.
	if version < 15 {
		if _, exists := archive.Tables["backup_recoveries"]; exists {
			return ErrArchive
		}
		archive.Tables["backup_recoveries"] = []Row{}
	}
	// Version 17 carries each restore's latest discard. None was recorded before it.
	if version < 17 {
		for _, row := range archive.Tables["backup_recoveries"] {
			if _, exists := row["discard_operation_id"]; exists {
				return ErrArchive
			}
			setValue(row, "discard_operation_id", nil)
		}
	}
	// Version 19 carries each destination's server. An older archive's are bound as migration
	// 00075 binds a database's.
	if version < 19 {
		if err := bindArchiveDestinations(archive); err != nil {
			return err
		}
	}
	// Version 21 carries managed storage accounts and their keys. None existed before it, and a
	// revoked server had been revoked once.
	if version < 21 {
		for _, name := range []string{"backup_storage_accounts", "backup_storage_keys"} {
			if _, exists := archive.Tables[name]; exists {
				return ErrArchive
			}
			archive.Tables[name] = []Row{}
		}
		if archive.Installation != uuid.Nil {
			return ErrArchive
		}
		for _, row := range archive.Tables["backup_destinations"] {
			if _, exists := row["account_id"]; exists {
				return ErrArchive
			}
			setValue(row, "account_id", nil)
		}
		for _, row := range archive.Tables["servers"] {
			var revoked bool
			if _, exists := row["revocations"]; exists || json.Unmarshal(row["revoked"], &revoked) != nil {
				return ErrArchive
			}
			revocations := 0
			if revoked {
				revocations = 1
			}
			setValue(row, "revocations", revocations)
		}
	}
	// Version 24 carries each storage account's key rotation. An older archive's accounts take the
	// 90 days migration 00079 gave every account.
	if version < 24 {
		for _, row := range archive.Tables["backup_storage_accounts"] {
			if _, exists := row["key_rotation_days"]; exists {
				return ErrArchive
			}
			setValue(row, "key_rotation_days", 90)
		}
	}
	return nil
}
