package execution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/jackc/pgx/v5"
)

// A management backup can contain the next frozen segment before its primary
// acknowledged retention. Reuse that exact envelope and child headers rather
// than regenerating a competing segment at the same sequence.
func (s *Service) recoverSavedCheckpoints(ctx context.Context, tx pgx.Tx, before portability.Archive, source *recoverySource) error {
	for _, row := range before.Tables["execution_operations"] {
		var current api.ExecutionCommand
		if json.Unmarshal(row["command"], &current) != nil {
			return portability.ErrArchive
		}
		retained, selected := source.Roots[current.Id]
		if !selected {
			continue
		}
		if current.PlanHash != retained.RootHash || current.ParentId != nil {
			return errors.New("saved root conflicts with the host checkpoint")
		}
		if current.RecoveryCheckpoint == nil {
			continue
		}
		var known deployment.RecoveryDescriptor
		if recoveryDecode(*current.RecoveryCheckpoint, &known) != nil || known.Validate() != nil || !deployment.SameRecoveryRoot(known, retained) {
			return errors.New("saved root checkpoint conflicts with host evidence")
		}
		source.KnownSequences[current.Id] = known.Sequence
		for sequence := retained.Sequence + 1; sequence <= known.Sequence; sequence++ {
			var descriptor, envelope []byte
			var phase string
			var acknowledged bool
			if err := tx.QueryRow(ctx, `SELECT descriptor,envelope,phase,acknowledged FROM cross_host_checkpoints WHERE workspace_id=$1 AND parent_id=$2 AND sequence=$3`, before.Workspace, current.Id, sequence).Scan(&descriptor, &envelope, &phase, &acknowledged); err != nil {
				return err
			}
			if acknowledged {
				return errors.New("management acknowledged metadata missing from the primary's retained evidence")
			}
			var next deployment.RecoveryDescriptor
			if recoveryDecode(descriptor, &next) != nil || next.Validate() != nil || !deployment.SameRecoveryRoot(next, retained) || next.Sequence != sequence || next.PreviousHash != retained.Digest || len(envelope) != next.Bytes || deployment.RecoveryDigest(envelope) != next.Digest {
				return errors.New("saved checkpoint suffix is incomplete or divergent")
			}
			decoded, err := s.Store.Keys.Open(envelope, recoveryIdentity(next))
			if err != nil {
				return errors.New("saved checkpoint requires its original management keyring")
			}
			var segment recoveryContinuation
			if recoveryDecode(decoded, &segment) != nil || segment.Phase != phase || len(segment.Children) == 0 || len(segment.Children) > 4 || len(segment.Approvals) > 1 {
				clear(decoded)
				return errors.New("invalid saved checkpoint continuation")
			}
			_, err = tx.Exec(ctx, `INSERT INTO recovery_input_checkpoints(id,parent_id,sequence,descriptor,envelope,decoded,primary_source,retained) VALUES($1,$2,$3,$4,$5,$6,$7,false)`, next.CheckpointID, next.ParentID, next.Sequence, descriptor, envelope, decoded, current.ServerId)
			clear(decoded)
			if err != nil {
				return err
			}
			retained = next
		}
		if known.Sequence >= retained.Sequence && deployment.Hash(known) != deployment.Hash(retained) {
			return errors.New("saved root does not match its complete checkpoint suffix")
		}
		source.Roots[current.Id] = retained
	}
	return nil
}
