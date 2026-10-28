package execution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func validateRecoveryRestrictionEvidence(ctx context.Context, tx pgx.Tx, participants map[uuid.UUID]deployment.RecoveryHandover) error {
	var missing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_standalone_restrictions r WHERE (r.source->>'report_sequence')::bigint>0 AND r.source->>'state' NOT IN ('succeeded','failed','cancelled') AND NOT EXISTS(SELECT 1 FROM recovery_input_proofs p WHERE p.id=r.id AND p.parent_id='00000000-0000-0000-0000-000000000000'))`).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return errors.New("a delivered standalone removal lacks retained progress evidence; provide compatible newer management metadata or host evidence")
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT server_id FROM recovery_input_proofs WHERE parent_id='00000000-0000-0000-0000-000000000000' AND report->>'state' NOT IN ('succeeded','failed','cancelled')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var server uuid.UUID
		if err := rows.Scan(&server); err != nil {
			return err
		}
		if _, participant := participants[server]; !participant {
			return errors.New("a nonparticipant has an unfinished local network removal; let the admitted removal settle locally, then stop and export that host again before recovery")
		}
	}
	return rows.Err()
}

func collectRecoveryRestrictions(ctx context.Context, tx pgx.Tx, archives ...portability.Archive) error {
	known := map[uuid.UUID]api.ExecutionCommand{}
	for _, archive := range archives {
		for _, row := range archive.Tables["execution_operations"] {
			var c api.ExecutionCommand
			if json.Unmarshal(row["command"], &c) != nil {
				return portability.ErrArchive
			}
			prior, exists := known[c.Id]
			if exists && (standaloneRestrictionFrame(prior) || standaloneRestrictionFrame(c)) && (prior.PlanHash != c.PlanHash || prior.ServerId != c.ServerId || prior.EnvironmentId != c.EnvironmentId || prior.ResourceId != c.ResourceId || prior.Generation != c.Generation || prior.Kind != c.Kind || prior.Action != c.Action || !prior.Deadline.Equal(c.Deadline)) {
				return errors.New("standalone removal conflicts with known execution history")
			}
			known[c.Id] = c
			if !standaloneRestrictionFrame(c) {
				continue
			}
			var id, server uuid.UUID
			if json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["server_id"], &server) != nil || id != c.Id || server != c.ServerId {
				return portability.ErrArchive
			}
			raw, _ := json.Marshal(row)
			if _, err := tx.Exec(ctx, `INSERT INTO recovery_standalone_restrictions(id,source) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET source=CASE WHEN recovery_standalone_restrictions.source IS NULL OR (excluded.source->>'report_sequence')::bigint>=(recovery_standalone_restrictions.source->>'report_sequence')::bigint THEN excluded.source ELSE recovery_standalone_restrictions.source END`, id, raw); err != nil {
				return err
			}
		}
	}
	return nil
}

// The offline transaction retains original outcomes above, then temporarily
// removes active-index entries while merging snapshots. Every selected frame
// is restored before commit; no host or serving worker can observe this hold.
func holdRecoveryRestrictions(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE execution_operations SET state='cancelled',job_id=NULL WHERE workspace_id=$1 AND id IN (SELECT id FROM recovery_standalone_restrictions)`, ws)
	return err
}

func (s *Service) restoreStandaloneRestrictions(ctx context.Context, tx pgx.Tx, ws uuid.UUID, controllers map[uuid.UUID]int64) error {
	// Terminal proofs must never enter the active-restriction index, even
	// transiently, and must settle before the one live removal is restored.
	for _, terminal := range []bool{true, false} {
		var last uuid.UUID
		for {
			var id uuid.UUID
			var source, header, report []byte
			err := tx.QueryRow(ctx, `SELECT r.id,r.source,p.header,p.report FROM recovery_standalone_restrictions r LEFT JOIN recovery_input_proofs p ON p.id=r.id AND p.parent_id='00000000-0000-0000-0000-000000000000' WHERE r.id>$1 AND ((CASE WHEN p.report IS NOT NULL AND (p.report->>'sequence')::bigint>=COALESCE((r.source->>'report_sequence')::bigint,0) THEN p.report->>'state' ELSE r.source->>'state' END) IN ('succeeded','failed','cancelled'))=$2 ORDER BY r.id LIMIT 1`, last, terminal).Scan(&id, &source, &header, &report)
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			if err != nil {
				return err
			}
			last = id
			var c api.ExecutionCommand
			initial := api.ExecutionState("queued")
			var row portability.Row
			if source != nil {
				if json.Unmarshal(source, &row) != nil || json.Unmarshal(row["command"], &c) != nil || json.Unmarshal(row["state"], &initial) != nil {
					return portability.ErrArchive
				}
			} else if recoveryDecode(header, &c) != nil {
				return errors.New("standalone removal has no frozen frame")
			}
			if header != nil {
				var h api.ExecutionCommand
				if recoveryDecode(header, &h) != nil || h.Id != c.Id || h.ServerId != c.ServerId || h.EnvironmentId != c.EnvironmentId || h.ResourceId != c.ResourceId || h.Kind != c.Kind || h.Action != c.Action || h.Generation != c.Generation || h.PlanHash != c.PlanHash || !h.Deadline.Equal(c.Deadline) {
					return errors.New("standalone proof conflicts with its stored frame")
				}
			}
			if !standaloneRestrictionFrame(c) || controllers[c.ServerId] == 0 {
				return errors.New("standalone removal has no compatible captured server authority")
			}
			if report == nil && !Terminal(initial) {
				initial = "queued"
			}
			if terminal {
				var proof api.AgentReport
				if report != nil && recoveryDecode(report, &proof) == nil && Terminal(proof.State) {
					initial = proof.State
				}
			}
			if source != nil && report != nil {
				var knownState api.ExecutionState
				var proof api.AgentReport
				if json.Unmarshal(row["state"], &knownState) != nil || recoveryDecode(report, &proof) != nil {
					return portability.ErrArchive
				}
				if Terminal(knownState) && knownState != proof.State {
					return errors.New("standalone proof conflicts with known terminal history")
				}
			}
			if err = storeRecoveryFrameState(ctx, tx, ws, c, initial); err != nil {
				return err
			}
			if source != nil {
				if _, err = tx.Exec(ctx, `UPDATE execution_operations dst SET state=$4,report_sequence=src.report_sequence,cancel_requested=src.cancel_requested,steps=src.steps,note=src.note,job_id=NULL FROM jsonb_populate_record(NULL::execution_operations,$3) src WHERE dst.workspace_id=$1 AND dst.id=$2`, ws, c.Id, source, initial); err != nil {
					return err
				}
			}
			if report != nil {
				if err = s.applyRecoveryProof(ctx, tx, ws, c, recoveringRoot{}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
