package portability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func DecodeRecovery(raw []byte) (Archive, error) {
	if len(raw) > MaxArchiveBytes {
		return Archive{}, ErrArchive
	}
	a, err := decodeArchiveMode(raw, true)
	if err == nil && a.Version < 5 {
		return a, ErrArchive
	}
	return a, err
}

func RecoveryStateHash(raw []byte) (string, error) {
	a, err := DecodeRecovery(raw)
	if err != nil {
		return "", err
	}
	return recoveryStateHash(a)
}

func recoveryStateHash(a Archive) (string, error) {
	b, err := stablejson.Marshal(struct {
		Name   string
		MFA    bool
		Tables map[string][]Row
	}{a.Name, a.RequireMFA, a.Tables})
	if err != nil {
		return "", err
	}
	return recoveryRowHash(b), nil
}

func recoveryRowHash(raw []byte) string {
	h := sha256.Sum256(stablejson.Normalize(raw))
	return hex.EncodeToString(h[:])
}

// recoveryMergeOrder applies a table's rows in an order its unique indexes accept one row at a
// time. Replacing a primary demotes one unit and promotes another, and primary_environment_unit
// allows one primary per environment, so demotions go first. A retired secret frees its name for a
// new one, and secrets_current_name allows one live secret per name, so retirements go first.
func recoveryMergeOrder(name string, rows []Row) ([]Row, error) {
	var first func(Row) (bool, error)
	switch name {
	case "deployment_units":
		first = func(row Row) (bool, error) {
			var primary bool
			if json.Unmarshal(row["primary_unit"], &primary) != nil {
				return false, ErrArchive
			}
			return !primary, nil
		}
	case "secrets":
		first = func(row Row) (bool, error) { return string(row["retired_at"]) != "null", nil }
	default:
		return rows, nil
	}
	early, late := []Row{}, []Row{}
	for _, row := range rows {
		ahead, err := first(row)
		if err != nil {
			return nil, err
		}
		if ahead {
			early = append(early, row)
		} else {
			late = append(late, row)
		}
	}
	return append(early, late...), nil
}

func recoveryKeys(name string) string {
	switch name {
	case "environment_layouts", "visual_runtime", "git_sources":
		return "workspace_id,environment_id"
	case "build_targets":
		return "workspace_id,environment_id,application_id"
	case "route_claims":
		return "server_id,claim"
	case "git_submissions":
		return "workspace_id,environment_id,revision_id"
	case "backup_cutovers":
		return "revision_id"
	case "network_members":
		return "workspace_id,server_id"
	case "network_endpoint_sources":
		return "endpoint_id,server_id,unit_id"
	case "network_plan_endpoints":
		return "plan_id,endpoint_id"
	case "network_plan_sources":
		return "plan_id,endpoint_id,server_id,unit_id"
	case "cross_host_approvals":
		return "workspace_id,request_key"
	case "cross_host_runtime":
		return "workspace_id,unit_id"
	case "cross_host_reservations":
		return "server_id"
	default:
		return "id"
	}
}

// recoveryErasure takes an incoming erasure of a stored secret version, and says whether either row
// is erased, which settles the version: its other columns never change.
func recoveryErasure(ctx context.Context, tx pgx.Tx, ws uuid.UUID, existing []byte, incoming Row) (bool, error) {
	var stored Row
	if err := json.Unmarshal(existing, &stored); err != nil {
		return false, err
	}
	if string(stored["erased_at"]) != "null" {
		return true, nil
	}
	if string(incoming["erased_at"]) == "null" {
		return false, nil
	}
	var id uuid.UUID
	var at time.Time
	if json.Unmarshal(incoming["id"], &id) != nil || json.Unmarshal(incoming["erased_at"], &at) != nil {
		return false, ErrArchive
	}
	return true, db.New(tx).EraseRecoveredSecretVersion(ctx, db.EraseRecoveredSecretVersionParams{ErasedAt: at, WorkspaceID: ws, ID: id})
}

func RecoveryImmutable(name string) bool {
	switch name {
	case "specification_revisions", "secret_versions", "execution_plans", "visual_requests", "visual_plans", "build_secret_versions", "build_artifacts", "backup_cutovers":
		return true
	}
	return false
}

// MergeRecovery is used only inside the offline recovery transaction after its
// process, owner and expected-state guards. It never disables immutable triggers
// or deletes history. Ciphertext is preserved with its original key identity.
func MergeRecovery(ctx context.Context, tx pgx.Tx, a Archive, owner uuid.UUID) error {
	links := map[string]map[string]jsontext.Value{"environments": {}, "execution_operations": {}}
	appliedOperations := map[string]jsontext.Value{}
	for _, t := range tables {
		rows, err := recoveryMergeOrder(t.Name, a.Tables[t.Name])
		if err != nil {
			return err
		}
		for _, sourceRow := range rows {
			row := maps.Clone(sourceRow)
			original, err := stablejson.Marshal(row)
			if err != nil {
				return err
			}
			keys := strings.Split(recoveryKeys(t.Name), ",")
			matches := []string{"dst.workspace_id=$2"}
			for _, k := range keys {
				matches = append(matches, "dst."+k+"=src."+k)
			}
			where := strings.Join(matches, " AND ")
			selected := strings.Split(t.Columns, ",")
			for i, c := range selected {
				selected[i] = "dst." + c
			}
			// inline-sql: identifiers come from the compiled portability table list
			query := fmt.Sprintf("SELECT to_jsonb(q) FROM (SELECT %s FROM %s dst CROSS JOIN jsonb_populate_record(NULL::%s,$1) src WHERE %s) q", strings.Join(selected, ","), t.Name, t.Name, where)
			var existing []byte
			err = tx.QueryRow(ctx, query, original, a.Workspace).Scan(&existing)
			found := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if found && RecoveryImmutable(t.Name) {
				if recoveryRowHash(existing) == recoveryRowHash(original) {
					continue
				}
				// Erasing its value is the only change a secret version takes, and it is never undone:
				// a checkpoint's erasure is taken, and a stored one kept.
				if t.Name == "secret_versions" {
					erased, err := recoveryErasure(ctx, tx, a.Workspace, existing, row)
					if err != nil {
						return err
					}
					if erased {
						continue
					}
				}
				attributed, err := db.New(tx).RecoveryRowAttribution(ctx, db.RecoveryRowAttributionParams{WorkspaceID: a.Workspace, TableName: t.Name, SourceHash: recoveryRowHash(original)})
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if attributed != recoveryRowHash(existing) {
					return fmt.Errorf("recovery conflicts with immutable %s", t.Name)
				}
				continue
			}
			// Recovery withdraws every cleanup request it brings back, as fencing the workspace does,
			// and one may name an account this database never had.
			if t.Name == "preview_requests" {
				setValue(row, "cleanup_requested", false)
				setValue(row, "cleanup_actor", nil)
				setValue(row, "cleanup_auth_version", nil)
				setValue(row, "cleanup_controller", nil)
			}
			for _, actor := range []string{"author_id", "actor_id", "policy_actor"} {
				if _, ok := row[actor]; ok {
					if found {
						var prior Row
						if err := json.Unmarshal(existing, &prior); err != nil {
							return err
						}
						row[actor] = prior[actor]
					} else {
						setValue(row, actor, owner)
					}
				}
			}
			// A recovered retirement or purge may name an account this database never had.
			for _, field := range []string{"retired_by", "purged_by"} {
				if t.Name != "environments" || string(row[field]) == "null" {
					continue
				}
				var account uuid.UUID
				if json.Unmarshal(row[field], &account) != nil {
					return ErrArchive
				}
				if _, err := db.New(tx).AccountByID(ctx, account); errors.Is(err, pgx.ErrNoRows) {
					setValue(row, field, owner)
				} else if err != nil {
					return err
				}
			}
			link := ""
			if t.Name == "environments" {
				link = "applied_revision_id"
			}
			if t.Name == "execution_operations" {
				link = "visual_plan_id"
			}
			if link != "" {
				id, err := rowString(row, "id")
				if err != nil {
					return err
				}
				links[t.Name][id] = row[link]
				setValue(row, link, nil)
				if t.Name == "environments" {
					appliedOperations[id] = row["runtime_operation_id"]
					setValue(row, "runtime_operation_id", nil)
				}
			}
			raw, err := stablejson.Marshal(row)
			if err != nil {
				return err
			}
			if !found {
				// inline-sql: identifiers come from the compiled portability table list
				_, err = tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM jsonb_populate_record(NULL::%s,$1)", t.Name, t.Columns, t.Columns, t.Name), raw)
			} else {
				// inline-sql: identifiers come from the compiled portability table list
				_, err = tx.Exec(ctx, fmt.Sprintf("UPDATE %s dst SET (%s)=(SELECT %s FROM jsonb_populate_record(NULL::%s,$1)) FROM jsonb_populate_record(NULL::%s,$1) src WHERE %s", t.Name, t.Columns, t.Columns, t.Name, t.Name, where), raw, a.Workspace)
			}
			if err != nil {
				return fmt.Errorf("recovery cannot merge %s: %w", t.Name, err)
			}
			if !found && RecoveryImmutable(t.Name) {
				var stored []byte
				if err = tx.QueryRow(ctx, query, raw, a.Workspace).Scan(&stored); err != nil {
					return err
				}
				// Cleanup can remove a recovered row without removing its attribution.
				if err = db.New(tx).UpsertRecoveryRowAttribution(ctx, db.UpsertRecoveryRowAttributionParams{WorkspaceID: a.Workspace, TableName: t.Name, SourceHash: recoveryRowHash(original), AttributedHash: recoveryRowHash(stored)}); err != nil {
					return err
				}
			}
		}
	}
	for name, rows := range links {
		for id, raw := range rows {
			var value *uuid.UUID
			if json.Unmarshal(raw, &value) != nil {
				return ErrArchive
			}
			if name == "environments" {
				var operation *uuid.UUID
				if json.Unmarshal(appliedOperations[id], &operation) != nil {
					return ErrArchive
				}
				environment, err := uuid.Parse(id)
				if err != nil {
					return ErrArchive
				}
				if err = db.New(tx).SetRecoveredEnvironmentRuntime(ctx, db.SetRecoveredEnvironmentRuntimeParams{AppliedRevisionID: value, RuntimeOperationID: operation, WorkspaceID: a.Workspace, ID: environment}); err != nil {
					return err
				}
				continue
			}
			operation, err := uuid.Parse(id)
			if err != nil {
				return ErrArchive
			}
			if err = db.New(tx).SetRecoveredOperationVisualPlan(ctx, db.SetRecoveredOperationVisualPlanParams{VisualPlanID: value, WorkspaceID: a.Workspace, ID: operation}); err != nil {
				return err
			}
		}
	}
	// A checkpoint from before archive version 8 brings volumes without the major they record.
	return backfillPostgresMajors(ctx, db.New(tx), a.Workspace)
}
