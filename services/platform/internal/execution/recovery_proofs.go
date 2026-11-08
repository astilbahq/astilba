package execution

import (
	"context"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) applyRecoveryProof(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand, root recoveringRoot) error {
	var headerRaw, reportRaw []byte
	// inline-sql: recovery session temporary table
	err := tx.QueryRow(ctx, `SELECT header,report FROM recovery_input_proofs WHERE server_id=$1 AND id=$2 AND parent_id=$3`, c.ServerId, c.Id, root.Command.Id).Scan(&headerRaw, &reportRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var header api.ExecutionCommand
	var report api.AgentReport
	if recoveryDecode(headerRaw, &header) != nil || recoveryDecode(reportRaw, &report) != nil || header.EnvironmentId != c.EnvironmentId || header.ResourceId != c.ResourceId || header.Kind != c.Kind || header.Generation != c.Generation || header.PlanHash != c.PlanHash || !header.Deadline.Equal(c.Deadline) || report.Id != c.Id || report.Generation != c.Generation || report.PlanHash != c.PlanHash || report.Sequence < 1 {
		return errors.New("host proof differs from the frozen execution frame")
	}
	if report.State != "running" && report.State != "needs_attention" && !Terminal(report.State) {
		return errors.New("host proof has an unsupported outcome")
	}
	q := db.New(tx)
	known, err := q.OperationReportState(ctx, db.OperationReportStateParams{WorkspaceID: ws, ID: c.Id})
	if err != nil {
		return err
	}
	knownSequence, knownState := known.ReportSequence, api.ExecutionState(known.State)
	if knownSequence == settledReportSequence {
		// inline-sql: recovery session temporary table
		_, err = tx.Exec(ctx, `UPDATE recovery_input_proofs SET applied=true WHERE server_id=$1 AND id=$2`, c.ServerId, c.Id)
		return err
	}
	if knownSequence > report.Sequence || knownSequence == report.Sequence && Terminal(knownState) && knownState != report.State {
		return errors.New("host proof conflicts with newer management knowledge of this operation")
	}
	var committed bool
	// inline-sql: recovery session temporary table
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_known_operations WHERE id=$1 AND report_sequence=$2 AND state=$3)`, c.Id, report.Sequence, report.State).Scan(&committed); err != nil {
		return err
	}
	if committed {
		if err = q.MergeOperationCancelRequest(ctx, db.MergeOperationCancelRequestParams{CancelRequested: header.CancelRequested, WorkspaceID: ws, ID: c.Id}); err != nil {
			return err
		}
		// inline-sql: recovery session temporary table
		_, err = tx.Exec(ctx, `UPDATE recovery_input_proofs SET applied=true WHERE server_id=$1 AND id=$2`, c.ServerId, c.Id)
		return err
	}
	if err = s.upgradeRecoveryProof(ctx, tx, c, &report); err != nil {
		return err
	}
	if err = s.crossHostReport(ctx, tx, c, &report); err != nil {
		return err
	}
	reportCommand := c
	reportCommand.ControllerGeneration = header.ControllerGeneration
	if err = s.networkReport(ctx, tx, ws, reportCommand, &report, false); err != nil {
		return err
	}
	steps, _ := stablejson.Marshal(report.Steps)
	if err = q.ApplyRecoveryProof(ctx, db.ApplyRecoveryProofParams{State: string(report.State), ReportSequence: report.Sequence, Steps: steps, Note: report.Note, CancelRequested: header.CancelRequested, WorkspaceID: ws, ID: c.Id}); err != nil {
		return err
	}
	// inline-sql: recovery session temporary table
	_, err = tx.Exec(ctx, `UPDATE recovery_input_proofs SET applied=true WHERE server_id=$1 AND id=$2`, c.ServerId, c.Id)
	return err
}

func (s *Service) upgradeRecoveryProof(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, r *api.AgentReport) error {
	if c.Kind != "upgrade" {
		if r.Upgrade != nil {
			return errors.New("upgrade outcome has no matching upgrade frame")
		}
		return nil
	}
	return s.upgradeReport(ctx, tx, c, r)
}

func (s *Service) restoreRecoveryProofs(ctx context.Context, tx pgx.Tx, ws uuid.UUID, roots map[uuid.UUID]recoveringRoot, hosts map[uuid.UUID]deployment.RecoveryEvidence) error {
	for parent, root := range roots {
		var last uuid.UUID
		for {
			var headerRaw []byte
			var id uuid.UUID
			var applied bool
			// inline-sql: recovery session temporary table
			err := tx.QueryRow(ctx, `SELECT id,header,applied FROM recovery_input_proofs WHERE parent_id=$1 AND id>$2 ORDER BY id LIMIT 1`, parent, last).Scan(&id, &headerRaw, &applied)
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			if err != nil {
				return err
			}
			last = id
			var header api.ExecutionCommand
			if recoveryDecode(headerRaw, &header) != nil {
				return errors.New("invalid retained recovery header")
			}
			if applied {
				continue
			}
			payload, retire, err := recoveryMetadataPayload(root, header, hosts[header.ServerId])
			if err != nil {
				return err
			}
			header.Crosshost = payload.Crosshost
			if err = storeRecoveryFrame(ctx, tx, ws, header); err != nil {
				return err
			}
			if retire {
				var raw []byte
				// inline-sql: recovery session temporary table
				if err = tx.QueryRow(ctx, `SELECT report FROM recovery_input_proofs WHERE server_id=$1 AND id=$2`, header.ServerId, header.Id).Scan(&raw); err != nil {
					return err
				}
				var report api.AgentReport
				if recoveryDecode(raw, &report) != nil || payload.Crosshost == nil {
					return errors.New("invalid superseded recovery metadata proof")
				}
				var body deployment.CrossHostCommand
				if json.Unmarshal(*payload.Crosshost, &body) != nil {
					return errors.New("invalid recovery metadata payload")
				}
				if body.Phase == "checkpoint" && report.State == "succeeded" {
					return errors.New("an acknowledged checkpoint requires its retained capsule")
				}
				if !Terminal(report.State) {
					report.State = "cancelled"
					report.Note = "Superseded metadata delivery or replay grant; guarded recovery resumes the retained phase without inferring any data effect."
					report.Sequence++
					raw, _ = stablejson.Marshal(report)
					// inline-sql: recovery session temporary table
					if _, err = tx.Exec(ctx, `UPDATE recovery_input_proofs SET report=$3 WHERE server_id=$1 AND id=$2`, header.ServerId, header.Id, raw); err != nil {
						return err
					}
				}
			}
			if err = s.applyRecoveryProof(ctx, tx, ws, header, root); err != nil {
				return err
			}
		}
	}
	return nil
}

// Only no-effect controller metadata may be reconstructed outside the retained
// phase chain. An interrupted data command must always match a frozen child.
func recoveryMetadataPayload(root recoveringRoot, header api.ExecutionCommand, host deployment.RecoveryEvidence) (api.ExecutionCommand, bool, error) {
	fail := errors.New("host holds an operation absent from its frozen continuation history")
	if header.Kind != "crosshost" || header.Action != "inspect" || header.ParentId == nil || *header.ParentId != root.Command.Id || header.EnvironmentId != root.Command.EnvironmentId || header.RecoveryCheckpoint == nil {
		return header, false, fail
	}
	var d deployment.RecoveryDescriptor
	if json.Unmarshal(*header.RecoveryCheckpoint, &d) != nil || d.Validate() != nil || !deployment.SameRecoveryRoot(d, root.Descriptor) {
		return header, false, fail
	}
	priorOpen := generation(header.ControllerGeneration) > 0 && generation(header.ControllerGeneration) <= host.Controller && header.Id == deployment.RecoveryOpenID(root.Command.Id, header.ResourceId, generation(header.ControllerGeneration))
	closed := header.Id == deployment.RecoveryCloseID(root.Command.Id, header.ResourceId)
	for _, unit := range root.Body.Plan.Distributed.Units {
		if unit.Plan.ServerID != header.ServerId || unit.Plan.UnitID != header.ResourceId {
			continue
		}
		var body deployment.CrossHostCommand
		retire := priorOpen
		if closed {
			if !unit.Plan.Local.Primary {
				return header, false, fail
			}
			body = deployment.CrossHostCommand{ParentID: root.Command.Id, GroupHash: d.RootHash, Phase: "checkpoint-closed", Unit: unit}
		} else if priorOpen {
			var err error
			body, err = recoveryOpenBody(root.Command, root.Body, unit)
			if err != nil {
				return header, false, err
			}
		} else {
			if !unit.Plan.Local.Primary || !isRecoveryMetadataFrame(root, header) {
				return header, false, fail
			}
			if d.Sequence > root.Descriptor.Sequence {
				if !deployment.UnretainedRecoveryDelivery(header.Id, d, root.Descriptor) {
					return header, false, fail
				}
				retire = true
			}
			body = deployment.CrossHostCommand{ParentID: root.Command.Id, GroupHash: root.Body.Plan.Hash, Phase: "checkpoint", Unit: unit}
			if root.Body.Maintenance != nil {
				for _, candidate := range root.Body.Maintenance.Units {
					if candidate.Unit.Plan.ServerID == header.ServerId {
						payload := maintenanceChildPayload(root.Command, metadataRecoveryPhase(root, header), root.Body.Maintenance, candidate)
						if payload.Crosshost == nil || json.Unmarshal(*payload.Crosshost, &body) != nil {
							return header, false, fail
						}
					}
				}
			}
		}
		if body.Digest() != header.PlanHash {
			return header, false, errors.New("metadata proof differs from its frozen root")
		}
		raw, _ := stablejson.Marshal(body)
		header.Crosshost = rawMessage(raw)
		return header, retire, nil
	}
	return header, false, fail
}
