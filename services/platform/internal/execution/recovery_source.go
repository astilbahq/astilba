package execution

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type recoverySource struct {
	Archive        portability.Archive
	Hosts          map[uuid.UUID]deployment.RecoveryEvidence
	Roots          map[uuid.UUID]deployment.RecoveryDescriptor
	KnownSequences map[uuid.UUID]int64
	Projections    map[uuid.UUID]recoveryProjection
	Origins        appliedOrigins
}

func recoveryPrivateFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("recovery input must be a bounded private regular file")
	}
	return os.ReadFile(path)
}

func recoveryDecode(raw []byte, out any) error {
	if json.Unmarshal(raw, out, json.RejectUnknownMembers(true)) != nil {
		return errors.New("invalid recovery record encoding")
	}
	return nil
}

// Records are bounded individually and streamed through temporary SQL tables.
// Repeated owner recovery actions do not consume the workspace archive ceiling.
func loadRecoverySource(ctx context.Context, tx pgx.Tx, keys *cryptobox.Keyring, ws uuid.UUID, before portability.Archive, dirs []string) (recoverySource, error) {
	var bases portability.RecoveryBases
	out := recoverySource{Hosts: map[uuid.UUID]deployment.RecoveryEvidence{}, Roots: map[uuid.UUID]deployment.RecoveryDescriptor{}, KnownSequences: map[uuid.UUID]int64{}}
	if len(dirs) == 0 || len(dirs) > 100 {
		return out, errors.New("provide every required host export")
	}
	// inline-sql: recovery session temporary table
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE recovery_input_checkpoints(id uuid PRIMARY KEY,parent_id uuid NOT NULL,sequence bigint NOT NULL,descriptor jsonb NOT NULL,envelope bytea NOT NULL,decoded bytea NOT NULL,PRIMARY_SOURCE uuid NOT NULL,retained boolean NOT NULL DEFAULT true,captured_at timestamptz,UNIQUE(parent_id,sequence)) ON COMMIT DROP; CREATE TEMP TABLE recovery_input_proofs(server_id uuid NOT NULL,id uuid NOT NULL,parent_id uuid NOT NULL,header jsonb NOT NULL,report jsonb NOT NULL,checkpoint text NOT NULL,applied boolean NOT NULL DEFAULT false,PRIMARY KEY(server_id,id)) ON COMMIT DROP`); err != nil {
		return out, err
	}
	// A server revoked in the restored management may be gone or hostile, so its export counts only
	// as proof of which machine it is, and for the sealed capsules it holds of its roots: beyond
	// what management kept of them, or of roots management never saw. What it claims is not read. An export that can't be read at all still stops recovery; leave it
	// out, since a revoked server's export is never required.
	revoked, err := recoveryRevokedServers(before)
	if err != nil {
		return out, err
	}
	// The roots whose sealed checkpoints management kept, which it supplies itself.
	held := map[uuid.UUID][]heldCapsule{}
	for _, row := range before.Tables["execution_operations"] {
		command, err := recoveryValue[api.ExecutionCommand](row, "command")
		if err != nil {
			return out, err
		}
		if command.RecoveryCheckpoint != nil {
			held[command.Id] = nil
		}
	}
	for _, dir := range dirs {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return out, errors.New("host recovery export directory must be private and not a symlink")
		}
		raw, err := recoveryPrivateFile(filepath.Join(dir, "host.json"), 2<<20)
		if err != nil {
			return out, err
		}
		var host deployment.RecoveryEvidence
		if recoveryDecode(raw, &host) != nil || host.ServerID == uuid.Nil || host.MachineID == uuid.Nil {
			return out, errors.New("invalid host recovery evidence")
		}
		if _, duplicate := out.Hosts[host.ServerID]; duplicate {
			return out, errors.New("duplicate host recovery evidence")
		}
		if revoked[host.ServerID] {
			out.Hosts[host.ServerID] = revokedEvidence(host)
			// Management's own chain is the floor for a revoked primary's roots it kept, so the host
			// can't truncate them; what the host holds beyond it is set aside to extend it. For any
			// other root the host holds the only copy. Management sealed both, so the host can only
			// replay them.
			if err = loadExportCapsules(ctx, tx, keys, ws, dir, host, false, held, &out, &bases); err != nil {
				return out, err
			}
			continue
		}
		if host.Version != 1 || host.Controller < 0 || len(host.ProofDigest) != 64 || len(host.Units) > 100 || len(host.Schedules) > 100 {
			return out, errors.New("invalid host recovery evidence")
		}
		out.Hosts[host.ServerID] = host
		for _, network := range host.Networks {
			if network.Validate() != nil || network.ServerID != host.ServerID || network.ControllerGeneration > host.Controller {
				return out, errors.New("host network authority differs from its evidence")
			}
		}
		if err = loadRecoveryProofs(ctx, tx, dir, host); err != nil {
			return out, err
		}
		if err = loadExportCapsules(ctx, tx, keys, ws, dir, host, true, nil, &out, &bases); err != nil {
			return out, err
		}
	}
	if err = loadRevokedPrimaryRoots(ctx, tx, keys, ws, before, revoked, held, &out, &bases); err != nil {
		return out, err
	}
	// inline-sql: recovery session temporary table
	rows, err := tx.Query(ctx, `SELECT descriptor,primary_source FROM recovery_input_checkpoints ORDER BY parent_id,sequence`)
	if err != nil {
		return out, err
	}
	primaries := map[uuid.UUID]uuid.UUID{}
	for rows.Next() {
		var raw []byte
		var d deployment.RecoveryDescriptor
		var primary uuid.UUID
		if err = rows.Scan(&raw, &primary); err != nil {
			rows.Close()
			return out, err
		}
		if json.Unmarshal(raw, &d) != nil {
			rows.Close()
			return out, errors.New("invalid recovery descriptor")
		}
		previous, exists := out.Roots[d.ParentID]
		if !exists && d.Sequence != 0 || exists && (primaries[d.ParentID] != primary || !deployment.SameRecoveryRoot(previous, d) || d.Sequence != previous.Sequence+1 || d.PreviousHash != previous.Digest) {
			rows.Close()
			return out, errors.New("recovery continuation chain is incomplete or divergent")
		}
		out.Roots[d.ParentID] = d
		primaries[d.ParentID] = primary
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if err = requireRecoverableRoots(out, revoked); err != nil {
		return out, err
	}
	out.Archive = bases.Archive()
	if out.Archive.Workspace != ws {
		return out, errors.New("no compatible base recovery capsule was supplied")
	}
	for _, host := range out.Hosts {
		// A revoked host's export decides nothing, so it can't demand a chain either.
		if revoked[host.ServerID] {
			continue
		}
		for _, root := range host.Roots {
			if !root.Primary {
				continue
			}
			if supplied, ok := out.Roots[root.Descriptor.ParentID]; !ok || deployment.Hash(supplied) != deployment.Hash(root.Descriptor) {
				return out, errors.New("the primary's complete retained checkpoint chain is required")
			}
		}
		if host.ActiveParent != uuid.Nil {
			if _, ok := out.Roots[host.ActiveParent]; !ok {
				return out, errors.New("another active host parent requires its compatible primary capsule")
			}
		}
	}
	return out, nil
}

// loadExportCapsules stages the sealed capsules in one host export. Those of a root in held are set
// aside there instead, for management's own chain to decide on. A capsule must come from its root's
// primary: as the host claims, when claims is set, and in any case as the sealed base's frozen root
// says.
func loadExportCapsules(ctx context.Context, tx pgx.Tx, keys *cryptobox.Keyring, ws uuid.UUID, dir string, host deployment.RecoveryEvidence, claims bool, held map[uuid.UUID][]heldCapsule, out *recoverySource, bases *portability.RecoveryBases) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.capsule"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		id, err := uuid.Parse(strings.TrimSuffix(filepath.Base(path), ".capsule"))
		if err != nil {
			return err
		}
		raw, err := recoveryPrivateFile(filepath.Join(dir, id.String()+".json"), 8192)
		if err != nil {
			return err
		}
		var d deployment.RecoveryDescriptor
		if recoveryDecode(raw, &d) != nil || d.Validate() != nil || d.CheckpointID != id || d.WorkspaceID != ws || !slices.Contains(d.Participants, host.ServerID) {
			return errors.New("capsule descriptor differs from the requested workspace or host")
		}
		if candidates, kept := held[d.ParentID]; kept {
			envelope, err := recoveryPrivateFile(path, deployment.RecoveryEnvelopeLimit)
			if err != nil {
				return err
			}
			// A copy that isn't what its descriptor says can't extend anything, like any forgery.
			if len(envelope) == d.Bytes && deployment.RecoveryDigest(envelope) == d.Digest {
				held[d.ParentID] = append(candidates, heldCapsule{descriptor: d, raw: raw, envelope: envelope, host: host.ServerID})
			}
			continue
		}
		primary := !claims
		for _, root := range host.Roots {
			if claims && root.Primary && deployment.SameRecoveryRoot(root.Descriptor, d) {
				primary = true
			}
		}
		if !primary {
			return errors.New("recovery capsule was not exported by its assigned primary")
		}
		envelope, err := recoveryPrivateFile(path, deployment.RecoveryEnvelopeLimit)
		if err != nil || len(envelope) != d.Bytes || deployment.RecoveryDigest(envelope) != d.Digest {
			return errors.New("recovery capsule differs from its descriptor")
		}
		if err = addRecoveryCheckpoint(ctx, tx, keys, ws, out, bases, d, raw, envelope, host.ServerID); err != nil {
			return err
		}
	}
	return nil
}

// addRecoveryCheckpoint opens one sealed checkpoint of a root whose primary is primary, checks it
// against its descriptor and frozen root, and stages it for the chain.
func addRecoveryCheckpoint(ctx context.Context, tx pgx.Tx, keys *cryptobox.Keyring, ws uuid.UUID, out *recoverySource, bases *portability.RecoveryBases, d deployment.RecoveryDescriptor, raw, envelope []byte, primary uuid.UUID) error {
	var captured *time.Time
	decoded, err := keys.Open(envelope, recoveryIdentity(d))
	if err != nil {
		return errors.New("the original management recovery keyring is required")
	}
	if d.Sequence == 0 {
		uncompressed, err := portability.DecompressRecovery(decoded)
		clear(decoded)
		if err != nil {
			return err
		}
		decoded = uncompressed
		a, err := portability.DecodeRecovery(decoded)
		if err != nil {
			clear(decoded)
			return err
		}
		if a.Workspace != ws {
			return errors.New("capsule archive belongs to another workspace")
		}
		var parent api.ExecutionCommand
		found := false
		for _, row := range a.Tables["execution_operations"] {
			var operation uuid.UUID
			if json.Unmarshal(row["id"], &operation) != nil {
				return portability.ErrArchive
			}
			if operation == d.ParentID {
				if json.Unmarshal(row["command"], &parent) != nil {
					return portability.ErrArchive
				}
				found = true
			}
		}
		var root coordinationCommand
		if !found || parent.Crosshost == nil || json.Unmarshal(*parent.Crosshost, &root) != nil || Hash(root) != parent.PlanHash || parent.PlanHash != d.RootHash || parent.ServerId != primary || root.Plan.ID != d.GroupID || root.Plan.Hash != d.GroupHash || root.Plan.Distributed == nil || root.Plan.Validate(true) != nil {
			return errors.New("base capsule does not contain its frozen root")
		}
		units := root.Plan.Distributed.Units
		if root.Maintenance != nil {
			units = root.Plan.ActiveUnits()
		}
		if len(units) != len(d.Participants) {
			return errors.New("recovery participant set differs from the frozen root")
		}
		for _, unit := range units {
			if !slices.Contains(d.Participants, unit.Plan.ServerID) {
				return errors.New("recovery participant is absent from its descriptor")
			}
		}
		captured = &a.CreatedAt
		if err = out.Origins.add(a, d.ParentID); err != nil {
			return err
		}
		if err = bases.Add(a); err != nil {
			return err
		}
	} else {
		var segment recoveryContinuation
		if recoveryDecode(decoded, &segment) != nil || len(segment.Phase) > 128 || segment.Phase == "" || len(segment.Children) == 0 || len(segment.Children) > 4 || len(segment.Approvals) > 1 {
			clear(decoded)
			return errors.New("invalid bounded recovery continuation")
		}
	}
	// inline-sql: recovery session temporary table
	_, err = tx.Exec(ctx, `INSERT INTO recovery_input_checkpoints(id,parent_id,sequence,descriptor,envelope,decoded,PRIMARY_SOURCE,captured_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, d.CheckpointID, d.ParentID, d.Sequence, raw, envelope, decoded, primary, captured)
	clear(decoded)
	if err != nil {
		return err
	}
	return nil
}

func loadRecoveryProofs(ctx context.Context, tx pgx.Tx, dir string, host deployment.RecoveryEvidence) error {
	path := filepath.Join(dir, "proofs.jsonl")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("host proof stream must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(file, hash))
	scanner.Buffer(make([]byte, 4096), 524288)
	for scanner.Scan() {
		var proof deployment.RecoveryProof
		var header api.ExecutionCommand
		var report api.AgentReport
		if recoveryDecode(scanner.Bytes(), &proof) != nil || recoveryDecode(proof.Header, &header) != nil || recoveryDecode(proof.Report, &report) != nil || header.Id != proof.ID || header.ServerId != host.ServerID || header.Id != report.Id || header.PlanHash != report.PlanHash || header.Generation != report.Generation || report.Sequence < 1 || report.Sequence >= portability.MaxCounter || header.Crosshost != nil || header.Backup != nil || header.Upgrade != nil || header.Visual != nil || header.Pipeline != nil || generation(header.ControllerGeneration) > host.Controller {
			return errors.New("host proof identity or redacted frame is invalid")
		}
		if header.ParentId == nil {
			if proof.ParentID != uuid.Nil || !standaloneRestrictionFrame(header) || report.Crosshost != nil || report.Upgrade != nil || report.Pipeline != nil || report.Observation != nil {
				return errors.New("only public standalone removals may be recovered without a parent")
			}
		} else if *header.ParentId != proof.ParentID {
			return errors.New("host proof parent differs from its frame")
		}
		// inline-sql: recovery session temporary table
		if _, err = tx.Exec(ctx, `INSERT INTO recovery_input_proofs(server_id,id,parent_id,header,report,checkpoint) VALUES($1,$2,$3,$4,$5,$6)`, host.ServerID, proof.ID, proof.ParentID, proof.Header, proof.Report, proof.Checkpoint); err != nil {
			return err
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != host.ProofDigest {
		return errors.New("host proof stream differs from its evidence digest")
	}
	return nil
}
