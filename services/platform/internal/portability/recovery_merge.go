package portability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func DecodeRecovery(raw []byte) (Archive, error) {
	if len(raw) > MaxArchiveBytes {
		return Archive{}, ErrArchive
	}
	a, err := decodeArchiveMode(raw, true)
	if err == nil && a.Version != 5 {
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
	b, err := json.Marshal(struct {
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
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) == nil {
		raw, _ = json.Marshal(value)
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
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
	links := map[string]map[string]json.RawMessage{"environments": {}, "execution_operations": {}}
	for _, t := range tables {
		for _, sourceRow := range a.Tables[t.Name] {
			row := maps.Clone(sourceRow)
			original, err := json.Marshal(row)
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
				var attributed string
				if err := tx.QueryRow(ctx, `SELECT attributed_hash FROM cross_host_recovery_rows WHERE workspace_id=$1 AND table_name=$2 AND source_hash=$3`, a.Workspace, t.Name, recoveryRowHash(original)).Scan(&attributed); err != nil || attributed != recoveryRowHash(existing) {
					return fmt.Errorf("recovery conflicts with immutable %s", t.Name)
				}
				continue
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
			}
			raw, err := json.Marshal(row)
			if err != nil {
				return err
			}
			if !found {
				_, err = tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM jsonb_populate_record(NULL::%s,$1)", t.Name, t.Columns, t.Columns, t.Name), raw)
			} else {
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
				if _, err = tx.Exec(ctx, `INSERT INTO cross_host_recovery_rows(workspace_id,table_name,source_hash,attributed_hash) VALUES($1,$2,$3,$4)`, a.Workspace, t.Name, recoveryRowHash(original), recoveryRowHash(stored)); err != nil {
					return err
				}
			}
		}
	}
	for name, rows := range links {
		field := "visual_plan_id"
		if name == "environments" {
			field = "applied_revision_id"
		}
		for id, raw := range rows {
			var value *uuid.UUID
			if json.Unmarshal(raw, &value) != nil {
				return ErrArchive
			}
			if _, err := tx.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s=$3 WHERE workspace_id=$1 AND id=$2", name, field), a.Workspace, id, value); err != nil {
				return err
			}
		}
	}
	return nil
}
