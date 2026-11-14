package execution

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"maps"
	"net/netip"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
func recoverySet(row portability.Row, key string, value any) { row[key], _ = stablejson.Marshal(value) }

func (s *Service) prepareRecoveryKnowledge(ctx context.Context, tx pgx.Tx, before portability.Archive, source *recoverySource, roots map[uuid.UUID]recoveringRoot) error {
	known, err := indexRecoveryRows(before)
	if err != nil {
		return err
	}
	incoming, err := indexRecoveryRows(source.Archive)
	if err != nil {
		return err
	}
	revoked, err := recoveryRevokedServers(before, source.Archive)
	if err != nil {
		return err
	}
	// inline-sql: recovery session temporary table
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
	projections, err := recoveryProjections(ctx, tx, before, source.Archive, roots, source.Origins, revoked)
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
					if err == nil {
						err = recoveryRetirementKnowledge(selected, prior, row, known["execution_operations"])
					}
				case "workspace_networks":
					if Hash(jsontext.Value(prior["id"])) != Hash(jsontext.Value(row["id"])) || Hash(jsontext.Value(prior["prefix"])) != Hash(jsontext.Value(row["prefix"])) {
						return errors.New("network identity conflicts with known management metadata")
					}
					err = recoveryMonotonic(selected, prior, row, nil, []string{"generation"})
				case "network_members":
					selected, err = recoveryMemberKnowledge(prior, row, source, revoked)
				case "secrets":
					recoverySecretKnowledge(selected, prior, row)
				case "managed_volumes":
					err = recoveryMonotonic(selected, prior, row, []string{"deleted"}, nil)
					// Recorded once, by the first deployment of a PostgreSQL major on the volume.
					if err == nil && string(prior["postgres_major"]) == "null" {
						selected["postgres_major"] = row["postgres_major"]
					}
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
					err = recoveryUnitKnowledge(selected, prior, row)
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
			selected, err := endRecoveryGapRow(table, row, source.History, known, incoming)
			if err != nil {
				return err
			}
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
	if err = recoveryUnitOwnership(prepared); err != nil {
		return err
	}
	if err = settleRevokedServers(prepared, revoked); err != nil {
		return err
	}
	if err = dropReleasedPreviewSecrets(&prepared); err != nil {
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
	a, err := portability.CommandIdentity(prior["command"], portability.CommandIdentityVersion)
	if err != nil {
		return nil, portability.ErrArchive
	}
	b, err := portability.CommandIdentity(incoming["command"], portability.CommandIdentityVersion)
	if err != nil {
		return nil, portability.ErrArchive
	}
	if a != b {
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
	outcome, sameRemoval, err := portability.MergeRuntimeOutcomes(prior, incoming)
	if err != nil {
		return nil, err
	}
	if !sameRemoval && (Terminal(sa) && Terminal(sb) && sa != sb || Terminal(sa) && !Terminal(sb) && qb > qa || Terminal(sb) && !Terminal(sa) && qa > qb) {
		return nil, errors.New("execution outcome conflicts with known management metadata")
	}
	out := maps.Clone(prior)
	if qb > qa || Terminal(sb) && !Terminal(sa) {
		for _, field := range []string{"state", "report_sequence", "steps", "note"} {
			out[field] = incoming[field]
		}
	}
	outcome.Set(out)
	if err = recoveryMonotonic(out, prior, incoming, []string{"cancel_requested"}, nil); err != nil {
		return nil, err
	}
	return out, nil
}

func recoveryMemberKnowledge(prior, incoming portability.Row, source *recoverySource, revoked map[uuid.UUID]bool) (portability.Row, error) {
	server, err := recoveryValue[uuid.UUID](prior, "server_id")
	if err != nil {
		return nil, err
	}
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
		// A revoked host may be gone or untrusted, so nothing can say which membership or key it holds,
		// and it no longer matters: the member stays disabled either way.
		if oldMatch == newMatch && !revoked[server] {
			return nil, errors.New("network membership differs without matching installed host evidence")
		}
		if newMatch && !oldMatch {
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
			if oldMatch == newMatch && !revoked[server] {
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
	if Hash(jsontext.Value(prior["plan"])) != Hash(jsontext.Value(incoming["plan"])) {
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
	// inline-sql: recovery session temporary table
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_known_operations k JOIN recovery_input_proofs p ON p.id=k.id WHERE k.report_sequence<>$1 AND (k.report_sequence>(p.report->>'sequence')::bigint OR k.report_sequence=(p.report->>'sequence')::bigint AND k.state IN ('succeeded','failed','cancelled') AND k.state<>p.report->>'state'))`, settledReportSequence).Scan(&conflict); err != nil {
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
			raw, err := db.New(tx).SupportedUpgradeStatus(ctx, db.SupportedUpgradeStatusParams{WorkspaceID: a.Workspace, ID: id})
			if err != nil {
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

// A unit only moves forward: created on a server, made primary, then, once a revoked primary is
// replaced, detached for good. Whichever archive has a unit further along knows more about it.
func recoveryUnitStage(row portability.Row) (int, *uuid.UUID, error) {
	server, err := recoveryValue[*uuid.UUID](row, "server_id")
	if err != nil {
		return 0, nil, err
	}
	primary, err := recoveryValue[bool](row, "primary_unit")
	if err != nil {
		return 0, nil, err
	}
	switch {
	case primary:
		return 1, server, nil
	case server == nil:
		return 2, nil, nil
	}
	return 0, server, nil
}

// recoveryUnitKnowledge keeps the unit's furthest stage. Within one stage a unit can gain a server
// but never change it.
func recoveryUnitKnowledge(selected, prior, incoming portability.Row) error {
	oldStage, oldServer, err := recoveryUnitStage(prior)
	if err != nil {
		return err
	}
	newStage, newServer, err := recoveryUnitStage(incoming)
	if err != nil {
		return err
	}
	switch {
	case newStage > oldStage:
		selected["server_id"], selected["primary_unit"] = incoming["server_id"], incoming["primary_unit"]
	case newStage < oldStage:
	case oldServer != nil && newServer != nil && *oldServer != *newServer:
		return errors.New("recovery cannot move an existing deployment unit to another server")
	case oldServer == nil:
		selected["server_id"] = incoming["server_id"]
	}
	return nil
}

// recoveryUnitOwnership gives an environment's managed volumes and resources to its primary unit,
// which is where every apply records them, once the merge has settled which unit that is.
func recoveryUnitOwnership(a portability.Archive) error {
	primary := map[uuid.UUID]jsontext.Value{}
	for _, row := range a.Tables["deployment_units"] {
		if isPrimary, err := recoveryValue[bool](row, "primary_unit"); err != nil {
			return err
		} else if !isPrimary {
			continue
		}
		env, err := recoveryValue[uuid.UUID](row, "environment_id")
		if err != nil {
			return err
		}
		primary[env] = row["id"]
	}
	for _, table := range []string{"managed_volumes", "managed_resources"} {
		for _, row := range a.Tables[table] {
			env, err := recoveryValue[uuid.UUID](row, "environment_id")
			if err != nil {
				return err
			}
			if unit, ok := primary[env]; ok {
				row["unit_id"] = unit
			}
		}
	}
	return nil
}
