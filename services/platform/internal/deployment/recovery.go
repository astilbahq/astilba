package deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

const RecoveryEnvelopeLimit = 1 << 20

// RecoveryDescriptor identifies management-encrypted metadata. Hosts retain the
// opaque envelope; only the management recovery keyring can open its contents.
type RecoveryDescriptor struct {
	Version      int         `json:"version"`
	WorkspaceID  uuid.UUID   `json:"workspaceId"`
	ParentID     uuid.UUID   `json:"parentId"`
	RootHash     string      `json:"rootHash"`
	GroupID      uuid.UUID   `json:"groupId"`
	GroupHash    string      `json:"groupHash"`
	BaseID       uuid.UUID   `json:"baseId"`
	CheckpointID uuid.UUID   `json:"checkpointId"`
	Sequence     int64       `json:"sequence"`
	PreviousHash string      `json:"previousHash,omitempty"`
	Digest       string      `json:"digest"`
	Bytes        int         `json:"bytes"`
	Participants []uuid.UUID `json:"participants"`
}

func (d RecoveryDescriptor) Validate() error {
	if d.Version != 1 || d.WorkspaceID == uuid.Nil || d.ParentID == uuid.Nil || d.GroupID == uuid.Nil || d.BaseID == uuid.Nil || d.CheckpointID == uuid.Nil || d.Sequence < 0 || d.Sequence > 1<<53 || d.Bytes < 1 || d.Bytes > RecoveryEnvelopeLimit || len(d.RootHash) != 64 || len(d.GroupHash) != 64 || len(d.Digest) != 64 || d.Sequence == 0 && (d.CheckpointID != d.BaseID || d.PreviousHash != "") || d.Sequence > 0 && len(d.PreviousHash) != 64 || len(d.Participants) == 0 || len(d.Participants) > 4 {
		return errors.New("invalid recovery checkpoint descriptor")
	}
	for _, hash := range []string{d.RootHash, d.GroupHash, d.Digest, d.PreviousHash} {
		if hash != "" {
			decoded, err := hex.DecodeString(hash)
			if err != nil || len(decoded) != sha256.Size {
				return errors.New("invalid recovery digest")
			}
		}
	}
	for index, id := range d.Participants {
		if id == uuid.Nil || slices.Contains(d.Participants[:index], id) {
			return errors.New("invalid recovery participants")
		}
	}
	return nil
}

func RecoveryDigest(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func SameRecoveryRoot(a, b RecoveryDescriptor) bool {
	return a.WorkspaceID == b.WorkspaceID && a.ParentID == b.ParentID && a.RootHash == b.RootHash && a.GroupID == b.GroupID && a.GroupHash == b.GroupHash && a.BaseID == b.BaseID && slices.Equal(a.Participants, b.Participants)
}

func RecoveryOpenID(parent, unit uuid.UUID, controller int64) uuid.UUID {
	return uuid.NewSHA1(parent, []byte("crosshost:recovery-open:"+strconv.FormatInt(controller, 10)+":"+unit.String()))
}

func RecoveryCloseID(parent, unit uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(parent, []byte("crosshost:checkpoint-closed:"+unit.String()))
}

// An unretained delivery is metadata for the next segment, whose effects were
// never released by the primary's acknowledgement. It is not a missing copy of
// a segment the primary already acknowledged.
func UnretainedRecoveryDelivery(id uuid.UUID, next, retained RecoveryDescriptor) bool {
	return next.Validate() == nil && SameRecoveryRoot(next, retained) && next.Sequence == retained.Sequence+1 && next.PreviousHash == retained.Digest && id == uuid.NewSHA1(next.CheckpointID, []byte("recovery-delivery"))
}

type RecoveryProof struct {
	ParentID   uuid.UUID       `json:"parentId"`
	ID         uuid.UUID       `json:"id"`
	Header     json.RawMessage `json:"header"`
	Checkpoint string          `json:"checkpoint"`
	Report     json.RawMessage `json:"report"`
}

type RecoveryRoot struct {
	Descriptor    RecoveryDescriptor `json:"descriptor"`
	EnvironmentID uuid.UUID          `json:"environmentId"`
	UnitID        uuid.UUID          `json:"unitId"`
	Primary       bool               `json:"primary"`
	UnitHash      string             `json:"unitHash"`
}

// RecoveryEvidence contains public identities and receipts, never hydrated
// command bodies, controller credentials or private network keys.
type RecoveryEvidence struct {
	Version      int                    `json:"version"`
	ServerID     uuid.UUID              `json:"serverId"`
	MachineID    uuid.UUID              `json:"machineId"`
	Controller   int64                  `json:"controller"`
	ActiveParent uuid.UUID              `json:"activeParent"`
	Units        map[string]string      `json:"units"`
	Schedules    map[string]int64       `json:"schedules"`
	Roots        []RecoveryRoot         `json:"roots"`
	ProofDigest  string                 `json:"proofDigest"`
	Networks     []hostnetwork.Manifest `json:"networks"`
}

type RecoveryAuthority struct {
	ParentID        uuid.UUID `json:"parentId"`
	RootHash        string    `json:"rootHash"`
	EvidenceHash    string    `json:"evidenceHash"`
	PriorController int64     `json:"priorController"`
	Controller      int64     `json:"controller"`
}

type RecoveryHandover struct {
	Authority     RecoveryAuthority    `json:"authority"`
	Descriptor    RecoveryDescriptor   `json:"descriptor"`
	UnitID        uuid.UUID            `json:"unitId"`
	EnvironmentID uuid.UUID            `json:"environmentId"`
	UnitHash      string               `json:"unitHash"`
	Primary       bool                 `json:"primary"`
	Network       hostnetwork.Manifest `json:"network"`
}
