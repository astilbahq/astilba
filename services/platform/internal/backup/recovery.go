package backup

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Recovery restores a snapshot from a policy's repository onto a server other than the policy's
// own, after that server was lost. Its installation is a view of the source policy: the policy's
// identity, destination and repository (so the repository path is the policy's), bound to the
// receiving server and the deployment plan applied there now. Nothing from the source server's run
// history reaches the receiving server: what it restores is decided by the snapshot alone, checked
// against this identity.
type Recovery struct {
	Installation Installation `json:"installation"`
	RunID        uuid.UUID    `json:"runId"`
	Snapshot     string       `json:"snapshot"`
	TargetID     uuid.UUID    `json:"targetId"`
	// ReceivedAt is when management first received a report of the snapshot, on its own clock.
	ReceivedAt time.Time `json:"receivedAt"`
}

// RecoveryClockAllowance is how far a manifest's creation time, on the writing server's clock, may
// be later than management's first receipt of its snapshot. An honest writer starts the manifest
// before the upload that reports it; the allowance covers that server's clock running fast, which
// can no longer be corrected once the server is lost.
const RecoveryClockAllowance = 15 * time.Minute

// MaxManifestBytes bounds the manifest a recovery reads out of a snapshot before trusting it.
const MaxManifestBytes = 65536

func (r Recovery) Digest() string {
	r.Installation.Access = Access{}
	r.Installation.RepositoryID = ""
	r.Installation.Next = time.Time{}
	return Hash(r)
}

func (r Recovery) Validate() error {
	p := r.Installation.Policy
	if p.Validate() != nil || p.Method == "container-job" || r.Installation.Destination.ID != p.DestinationID || r.RunID == uuid.Nil || r.TargetID == uuid.Nil || r.TargetID == p.ResourceID || !SnapshotID(r.Snapshot) || r.ReceivedAt.IsZero() {
		return errors.New("invalid recovery restore")
	}
	return nil
}

// Lifetime is a restore's, as Command.Lifetime.
func (Recovery) Lifetime() time.Duration { return max(commandLifetime, RestoreTimeout+commandMargin) }

// Admits checks a manifest read out of the snapshot against the policy it must have been written
// for, before anything is downloaded or created for it.
func (r Recovery) Admits(m Manifest) error {
	p := r.Installation.Policy
	if m.Recipe != Recipe || m.PolicyID != p.ID || m.EnvironmentID != p.EnvironmentID || m.ResourceID != p.ResourceID || m.Method != p.Method || m.RunID == uuid.Nil {
		return errors.New("snapshot was not written for this policy")
	}
	if m.Bytes <= 0 || m.Bytes > 1<<40 || !SnapshotID(m.SHA256) {
		return errors.New("snapshot archive size or digest is invalid")
	}
	// Only manifests the recipe writes, which the restore reports back as they are.
	bounded := m.Roles != nil && m.Extensions != nil && len(m.Roles) <= 100 && len(m.Extensions) <= 100 && len(m.Image) <= 512 && len(m.Database) <= 100 && len(m.Username) <= 100
	for _, r := range m.Roles {
		bounded = bounded && r.Name != "" && len(r.Name) <= 63
	}
	for _, x := range m.Extensions {
		bounded = bounded && x != "" && len(x) <= 100
	}
	if !bounded {
		return errors.New("snapshot manifest is outside its recipe's bounds")
	}
	if m.CreatedAt.IsZero() || m.CreatedAt.After(r.ReceivedAt.Add(RecoveryClockAllowance)) {
		return errors.New("snapshot claims to be newer than its first report")
	}
	return nil
}
