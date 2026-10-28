package deployment

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

type RestoreCutover struct {
	SourcePlanID   uuid.UUID `json:"sourcePlanId"`
	SourceHash     string    `json:"sourceHash"`
	PolicyID       uuid.UUID `json:"policyId"`
	RunID          uuid.UUID `json:"runId"`
	ManifestHash   string    `json:"manifestHash"`
	OldVolumeID    uuid.UUID `json:"oldVolumeId"`
	TargetVolumeID uuid.UUID `json:"targetVolumeId"`
}

func (r RestoreCutover) Validate() error {
	if r.SourcePlanID == uuid.Nil || len(r.SourceHash) != 64 || r.PolicyID == uuid.Nil || r.RunID == uuid.Nil || len(r.ManifestHash) != 64 || r.OldVolumeID == uuid.Nil || r.TargetVolumeID == uuid.Nil || r.OldVolumeID == r.TargetVolumeID {
		return errors.New("invalid restored-volume cutover identity")
	}
	return nil
}

// RestoreSpecification permits only replacing the restored volume's identity.
// Images, credentials, placement and all other service configuration stay fixed.
func RestoreSpecification(source domain.Specification, old, target uuid.UUID) (domain.Specification, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return source, err
	}
	var out domain.Specification
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	found := false
	for i := range out.Volumes {
		if out.Volumes[i].ID == old {
			out.Volumes[i].ID = target
			found = true
		}
	}
	if !found || old == target || target == uuid.Nil {
		return out, errors.New("restored volume is not in the source specification")
	}
	for i := range out.Applications {
		for j := range out.Applications[i].Mounts {
			if out.Applications[i].Mounts[j].VolumeID == old {
				out.Applications[i].Mounts[j].VolumeID = target
			}
		}
	}
	return out, nil
}

func RestoredGroup(source Plan, r RestoreCutover, id, revision uuid.UUID, expires time.Time) (Plan, error) {
	if r.Validate() != nil || source.Validate(true) != nil || source.Distributed == nil || source.ID != r.SourcePlanID || source.Hash != r.SourceHash {
		return Plan{}, errors.New("restore source differs from its frozen group")
	}
	spec, err := RestoreSpecification(source.Specification, r.OldVolumeID, r.TargetVolumeID)
	if err != nil {
		return Plan{}, err
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return Plan{}, err
	}
	var target Plan
	if err = json.Unmarshal(raw, &target); err != nil {
		return target, err
	}
	target.ID, target.RevisionID, target.Restore = id, revision, &r
	target.Specification, target.ExpiresAt = spec, expires
	target.ExpectedAppliedID = &source.RevisionID
	target.Release, target.ReleaseOptions = nil, nil
	target.HostChanges = true
	target.Effects = []string{"Stop and confirm application writers on every placement server before restoring the primary volume.", "Replace only the reviewed restored-volume mounts. Retain the old volume; reconciliation continues forward once activation begins.", "Mark the revision applied only after every writer has resumed and passed readiness."}
	for i := range target.Distributed.Units {
		u := &target.Distributed.Units[i]
		previous := source.Distributed.Units[i].Plan
		u.Previous = previous
		u.Plan.ID = uuid.NewSHA1(id, []byte("unit:"+u.Plan.ServerID.String()))
		u.Plan.RevisionID, u.Plan.ExpectedAppliedID = revision, &source.RevisionID
		u.Plan.Specification, u.Plan.ExpiresAt = spec, expires
		u.Plan.Release, u.Plan.ReleaseOptions = nil, nil
		u.Plan.Local.GroupID = id
		u.Plan = u.Plan.WithHash()
	}
	target.Distributed.Units = target.ActiveUnits()
	return target.WithHash(), nil
}
