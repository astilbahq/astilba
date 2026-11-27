package backup

import (
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
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
}

// RecoveryResult is what a succeeded restore put in its target volume (AgentReport.restore): the
// snapshot, the target, and the manifest read out of the snapshot that the restored data matches.
type RecoveryResult struct {
	Snapshot string    `json:"snapshot"`
	TargetID uuid.UUID `json:"targetId"`
	Manifest Manifest  `json:"manifest"`
	// RestoreImage is the PostgreSQL release that restored a logical snapshot. An agent from
	// before the release rule restored in the archive's own image and leaves it out.
	RestoreImage string `json:"restoreImage,omitempty"`
	// RestoreRoles is the restored cluster's role model, as for a run.
	RestoreRoles string `json:"restoreRoles,omitempty"`
}

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
	if p.Validate() != nil || p.Method == "container-job" || r.RunID == uuid.Nil || r.TargetID == uuid.Nil || !SnapshotID(r.Snapshot) {
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
	if m.PolicyID != p.ID || m.EnvironmentID != p.EnvironmentID || m.ResourceID != p.ResourceID || m.Method != p.Method {
		return errors.New("snapshot was not written for this policy")
	}
	return m.recipeBounded()
}

// ParseRecoveryResult reads a recorded restore's result, which must name the restore's own
// snapshot and target and hold a manifest of its policy and environment within the recipe's bounds.
func ParseRecoveryResult(raw []byte, policy, environment uuid.UUID, snapshot string, target uuid.UUID) (RecoveryResult, error) {
	var out RecoveryResult
	if json.Unmarshal(raw, &out) != nil || out.Snapshot != snapshot || out.TargetID != target || out.Manifest.PolicyID != policy || out.Manifest.EnvironmentID != environment {
		return out, errors.New("invalid restore result")
	}
	return out, out.Manifest.recipeBounded()
}

// ValidDumpSettings reports whether s holds a value for each of DumpSettings and nothing else, each
// at most 256 characters of printable ASCII. A backup refuses a database whose dump would run with
// any other.
func ValidDumpSettings(s map[string]string) bool {
	valid := len(s) == len(DumpSettings)
	for _, name := range DumpSettings {
		v, ok := s[name]
		valid = valid && ok && len(v) <= 256 && !strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r > 0x7e })
	}
	return valid
}

// recipeBounded checks what a manifest holds against what the recipe writes.
func (m Manifest) recipeBounded() error {
	if m.Recipe != Recipe || m.RunID == uuid.Nil {
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
		bounded = bounded && slices.Contains(Extensions, x)
	}
	if m.DumpSettings != nil {
		bounded = bounded && ValidDumpSettings(m.DumpSettings)
	}
	if !bounded {
		return errors.New("snapshot manifest is outside its recipe's bounds")
	}
	return nil
}
