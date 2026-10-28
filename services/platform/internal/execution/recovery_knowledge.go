package execution

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/netip"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type recoveryRows map[string]map[string]portability.Row

func indexRecoveryRows(a portability.Archive) (recoveryRows, error) {
	out := recoveryRows{}
	for table, rows := range a.Tables {
		out[table] = map[string]portability.Row{}
		for _, row := range rows {
			key, err := portability.RecoveryRowKey(table, row)
			if err != nil {
				return nil, err
			}
			out[table][key] = row
		}
	}
	return out, nil
}
func recoveryValue[T any](row portability.Row, key string) (T, error) {
	var value T
	if json.Unmarshal(row[key], &value) != nil {
		return value, portability.ErrArchive
	}
	return value, nil
}
func recoverySet(row portability.Row, key string, value any) { row[key], _ = json.Marshal(value) }

func (s *Service) prepareRecoveryKnowledge(ctx context.Context, tx pgx.Tx, before portability.Archive, source *recoverySource, roots map[uuid.UUID]recoveringRoot) error {
	known, err := indexRecoveryRows(before)
	if err != nil {
		return err
	}
	incoming, err := indexRecoveryRows(source.Archive)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TEMP TABLE recovery_known_operations(id uuid PRIMARY KEY,report_sequence bigint NOT NULL,state text NOT NULL) ON COMMIT DROP`); err != nil {
		return err
	}
	progress := [][]any{}
	for _, row := range before.Tables["execution_operations"] {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return err
		}
		sequence, err := recoveryValue[int64](row, "report_sequence")
		if err != nil {
			return err
		}
		state, err := recoveryValue[string](row, "state")
		if err != nil {
			return err
		}
		progress = append(progress, []any{id, sequence, state})
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"recovery_known_operations"}, []string{"id", "report_sequence", "state"}, pgx.CopyFromRows(progress)); err != nil {
		return err
	}
	projections, err := recoveryProjections(ctx, tx, before, source.Archive, roots)
	if err != nil {
		return err
	}
	prepared := source.Archive
	prepared.Tables = map[string][]portability.Row{}
	for table, rows := range source.Archive.Tables {
		prepared.Tables[table] = []portability.Row{}
		for _, row := range rows {
			key, err := portability.RecoveryRowKey(table, row)
			if err != nil {
				return err
			}
			prior, exists := known[table][key]
			selected := maps.Clone(row)
			if exists && !portability.RecoveryImmutable(table) {
				selected = maps.Clone(prior)
				switch table {
				case "execution_operations":
					selected, err = recoveryOperationKnowledge(prior, row)
				case "servers":
					err = recoveryMonotonic(selected, prior, row, []string{"revoked"}, []string{"controller_generation", "update_generation"})
				case "environments":
					err = recoveryMonotonic(selected, prior, row, nil, []string{"execution_generation"})
				case "workspace_networks":
					if Hash(json.RawMessage(prior["id"])) != Hash(json.RawMessage(row["id"])) || Hash(json.RawMessage(prior["prefix"])) != Hash(json.RawMessage(row["prefix"])) {
						return errors.New("network identity conflicts with known management metadata")
					}
					err = recoveryMonotonic(selected, prior, row, nil, []string{"generation"})
				case "network_members":
					selected, err = recoveryMemberKnowledge(prior, row, source)
				case "managed_volumes":
					err = recoveryMonotonic(selected, prior, row, []string{"deleted"}, nil)
				case "supported_upgrades":
					selected, err = recoveryUpgradeKnowledge(prior, row)
				case "backup_policies":
					selected, err = recoveryPolicyKnowledge(prior, row)
				case "pipeline_runs":
					if op, exists := incoming["execution_operations"][key]; exists {
						if oldOp, exists := known["execution_operations"][key]; exists {
							progress, err2 := recoveryOperationKnowledge(oldOp, op)
							if err2 != nil {
								return err2
							}
							if string(progress["report_sequence"]) != string(oldOp["report_sequence"]) || string(progress["state"]) != string(oldOp["state"]) {
								selected = maps.Clone(row)
							}
						} else {
							selected = maps.Clone(row)
						}
					}
				case "deployment_units":
					oldServer, err2 := recoveryValue[*uuid.UUID](prior, "server_id")
					if err2 != nil {
						return err2
					}
					newServer, err2 := recoveryValue[*uuid.UUID](row, "server_id")
					if err2 != nil {
						return err2
					}
					if oldServer != nil && newServer != nil && *oldServer != *newServer {
						return errors.New("recovery cannot move an existing deployment unit to another server")
					}
					if oldServer == nil {
						selected["server_id"] = row["server_id"]
					}
				}
				if err != nil {
					return err
				}
			}
			if keep, err := applyRecoveryProjection(table, selected, projections); err != nil {
				return err
			} else if keep {
				prepared.Tables[table] = append(prepared.Tables[table], selected)
			}
		}
	}
	for table, rows := range before.Tables {
		for _, row := range rows {
			key, err := portability.RecoveryRowKey(table, row)
			if err != nil {
				return err
			}
			if _, exists := incoming[table][key]; exists {
				continue
			}
			selected := maps.Clone(row)
			if keep, err := applyRecoveryProjection(table, selected, projections); err != nil {
				return err
			} else if keep {
				prepared.Tables[table] = append(prepared.Tables[table], selected)
			}
		}
	}
	if err = appendRecoveryProjectionRows(&prepared, projections); err != nil {
		return err
	}
	source.Archive = prepared
	source.Projections = projections
	return preflightRecoveryKnownProofs(ctx, tx)
}

func recoveryMonotonic(out, a, b portability.Row, flags, counters []string) error {
	for _, field := range flags {
		x, err := recoveryValue[bool](a, field)
		if err != nil {
			return err
		}
		y, err := recoveryValue[bool](b, field)
		if err != nil {
			return err
		}
		recoverySet(out, field, x || y)
	}
	for _, field := range counters {
		x, err := recoveryValue[int64](a, field)
		if err != nil {
			return err
		}
		y, err := recoveryValue[int64](b, field)
		if err != nil {
			return err
		}
		recoverySet(out, field, max(x, y))
	}
	return nil
}
func recoveryOperationKnowledge(prior, incoming portability.Row) (portability.Row, error) {
	var a, b api.ExecutionCommand
	if json.Unmarshal(prior["command"], &a) != nil || json.Unmarshal(incoming["command"], &b) != nil {
		return nil, portability.ErrArchive
	}
	a.ControllerGeneration, b.ControllerGeneration = nil, nil
	a.RecoveryCheckpoint, b.RecoveryCheckpoint = nil, nil
	a.CancelRequested, b.CancelRequested = false, false
	a.Deadline, b.Deadline = a.Deadline.UTC(), b.Deadline.UTC()
	if Hash(a) != Hash(b) {
		return nil, errors.New("execution identity conflicts with known management metadata")
	}
	qa, err := recoveryValue[int64](prior, "report_sequence")
	if err != nil {
		return nil, err
	}
	qb, err := recoveryValue[int64](incoming, "report_sequence")
	if err != nil {
		return nil, err
	}
	sa, err := recoveryValue[api.ExecutionState](prior, "state")
	if err != nil {
		return nil, err
	}
	sb, err := recoveryValue[api.ExecutionState](incoming, "state")
	if err != nil {
		return nil, err
	}
	if Terminal(sa) && Terminal(sb) && sa != sb || Terminal(sa) && !Terminal(sb) && qb > qa || Terminal(sb) && !Terminal(sa) && qa > qb {
		return nil, errors.New("execution outcome conflicts with known management metadata")
	}
	out := maps.Clone(prior)
	if qb > qa || Terminal(sb) && !Terminal(sa) {
		for _, field := range []string{"state", "report_sequence", "steps", "note"} {
			out[field] = incoming[field]
		}
	}
	if err = recoveryMonotonic(out, prior, incoming, []string{"cancel_requested"}, nil); err != nil {
		return nil, err
	}
	return out, nil
}
func recoveryMemberKnowledge(prior, incoming portability.Row, source *recoverySource) (portability.Row, error) {
	oldID, err := recoveryValue[uuid.UUID](prior, "membership_id")
	if err != nil {
		return nil, err
	}
	newID, err := recoveryValue[uuid.UUID](incoming, "membership_id")
	if err != nil {
		return nil, err
	}
	out := maps.Clone(prior)
	if oldID != newID {
		oldMatch, err := recoveryInstalledMember(prior, source.Hosts)
		if err != nil {
			return nil, err
		}
		newMatch, err := recoveryInstalledMember(incoming, source.Hosts)
		if err != nil {
			return nil, err
		}
		if oldMatch == newMatch {
			return nil, errors.New("network membership differs without matching installed host evidence")
		}
		if newMatch {
			out = maps.Clone(incoming)
		}
	} else {
		oldKey, err := recoveryValue[string](prior, "public_key")
		if err != nil {
			return nil, err
		}
		newKey, err := recoveryValue[string](incoming, "public_key")
		if err != nil {
			return nil, err
		}
		if oldKey == "" && newKey != "" {
			// Identification fills this immutable membership's previously unknown
			// public key. The checkpoint authenticates that management report.
			if !hostnetwork.PublicKey(newKey) {
				return nil, portability.ErrArchive
			}
			out["public_key"] = incoming["public_key"]
		} else if oldKey != "" && newKey != "" && oldKey != newKey {
			oldMatch, err := recoveryInstalledMember(prior, source.Hosts)
			if err != nil {
				return nil, err
			}
			newMatch, err := recoveryInstalledMember(incoming, source.Hosts)
			if err != nil {
				return nil, err
			}
			if oldMatch == newMatch {
				return nil, errors.New("network key differs without matching installed host evidence")
			}
			if newMatch {
				out["public_key"] = incoming["public_key"]
			}
		}
		a, err := recoveryValue[bool](prior, "enabled")
		if err != nil {
			return nil, err
		}
		b, err := recoveryValue[bool](incoming, "enabled")
		if err != nil {
			return nil, err
		}
		recoverySet(out, "enabled", a && b)
	}
	if err = recoveryMonotonic(out, prior, incoming, nil, []string{"generation"}); err != nil {
		return nil, err
	}
	return out, nil
}

func recoveryInstalledMember(row portability.Row, hosts map[uuid.UUID]deployment.RecoveryEvidence) (bool, error) {
	server, err := recoveryValue[uuid.UUID](row, "server_id")
	if err != nil {
		return false, err
	}
	network, err := recoveryValue[uuid.UUID](row, "network_id")
	if err != nil {
		return false, err
	}
	membership, err := recoveryValue[uuid.UUID](row, "membership_id")
	if err != nil {
		return false, err
	}
	key, err := recoveryValue[string](row, "public_key")
	if err != nil {
		return false, err
	}
	address, err := recoveryValue[string](row, "address")
	if err != nil {
		return false, err
	}
	if prefix, err := netip.ParsePrefix(address); err == nil {
		address = prefix.Addr().String()
	}
	for _, manifest := range hosts[server].Networks {
		if manifest.NetworkID == network && manifest.MembershipID == membership && manifest.ServerID == server && manifest.PublicKey == key && manifest.Address == address {
			return true, nil
		}
	}
	return false, nil
}
func recoveryUpgradeKnowledge(prior, incoming portability.Row) (portability.Row, error) {
	if Hash(json.RawMessage(prior["plan"])) != Hash(json.RawMessage(incoming["plan"])) {
		return nil, errors.New("upgrade plan conflicts with known management metadata")
	}
	a, err := recoveryValue[upgrade.Status](prior, "status")
	if err != nil {
		return nil, err
	}
	b, err := recoveryValue[upgrade.Status](incoming, "status")
	if err != nil {
		return nil, err
	}
	terminal := func(status upgrade.Status) bool {
		return status.Phase == "complete" || status.Phase == "returned" || status.Phase == "rejected"
	}
	if terminal(a) && terminal(b) && a.Phase != b.Phase || terminal(a) && !a.WritesPossible && b.WritesPossible || terminal(b) && !b.WritesPossible && a.WritesPossible {
		return nil, errors.New("upgrade outcome conflicts with known management metadata")
	}
	out := maps.Clone(prior)
	if !terminal(a) && terminal(b) || !a.WritesPossible && b.WritesPossible {
		out["status"] = incoming["status"]
	}
	return out, nil
}
func recoveryPolicyKnowledge(prior, incoming portability.Row) (portability.Row, error) {
	a, err := recoveryValue[backup.Policy](prior, "configuration")
	if err != nil {
		return nil, err
	}
	b, err := recoveryValue[backup.Policy](incoming, "configuration")
	if err != nil {
		return nil, err
	}
	out := maps.Clone(prior)
	if b.Generation > a.Generation {
		out["configuration"], out["encrypted_access"] = incoming["configuration"], incoming["encrypted_access"]
	}
	sa, err := recoveryValue[backup.Status](prior, "status")
	if err != nil {
		return nil, err
	}
	sb, err := recoveryValue[backup.Status](incoming, "status")
	if err != nil {
		return nil, err
	}
	qa, err := recoveryValue[int64](prior, "status_sequence")
	if err != nil {
		return nil, err
	}
	qb, err := recoveryValue[int64](incoming, "status_sequence")
	if err != nil {
		return nil, err
	}
	if sb.Generation > sa.Generation || sb.Generation == sa.Generation && qb > qa {
		out["status"], out["status_sequence"] = incoming["status"], incoming["status_sequence"]
	}
	return out, nil
}
func preflightRecoveryKnownProofs(ctx context.Context, tx pgx.Tx) error {
	var conflict bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_known_operations k JOIN recovery_input_proofs p ON p.id=k.id WHERE k.report_sequence>(p.report->>'sequence')::bigint OR k.report_sequence=(p.report->>'sequence')::bigint AND k.state IN ('succeeded','failed','cancelled') AND k.state<>p.report->>'state')`).Scan(&conflict); err != nil {
		return err
	}
	if conflict {
		return errors.New("host proof conflicts with newer management knowledge of this operation")
	}
	return nil
}
func validateRecoveryUpgradeKnowledge(ctx context.Context, tx pgx.Tx, archives ...portability.Archive) error {
	for _, a := range archives {
		for _, row := range a.Tables["supported_upgrades"] {
			id, err := recoveryValue[uuid.UUID](row, "id")
			if err != nil {
				return err
			}
			known, err := recoveryValue[upgrade.Status](row, "status")
			if err != nil {
				return err
			}
			var raw []byte
			if err = tx.QueryRow(ctx, `SELECT status FROM supported_upgrades WHERE workspace_id=$1 AND id=$2`, a.Workspace, id).Scan(&raw); err != nil {
				return err
			}
			var current upgrade.Status
			if json.Unmarshal(raw, &current) != nil {
				return portability.ErrArchive
			}
			terminal := known.Phase == "complete" || known.Phase == "returned" || known.Phase == "rejected"
			if known.WritesPossible && !current.WritesPossible || terminal && (known.Phase != current.Phase || known.Serving != current.Serving) {
				return errors.New("recovered upgrade would discard a known data outcome")
			}
		}
	}
	return nil
}
