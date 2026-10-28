// Package upgrade defines the bounded PostgreSQL 17 to 18 recipe. It contains
// no host effects; the agent owns isolation, migration and recovery.
package upgrade

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

const Recipe = 1

type Plan struct {
	Group            *deployment.Plan `json:"group,omitempty"`
	ID               uuid.UUID        `json:"id"`
	Recipe           int              `json:"recipe"`
	PolicyID         uuid.UUID        `json:"policyId"`
	ResourceID       uuid.UUID        `json:"resourceId"`
	TargetVolumeID   uuid.UUID        `json:"targetVolumeId"`
	TargetPlanID     uuid.UUID        `json:"targetPlanId"`
	TargetRevisionID uuid.UUID        `json:"targetRevisionId"`
	RetainDays       int              `json:"retainDays"`
	ExpiresAt        time.Time        `json:"expiresAt"`
	Source           deployment.Plan  `json:"source"`
}

type Status struct {
	Phase          string    `json:"phase"`
	Step           string    `json:"step"`
	Serving        string    `json:"serving"`
	WritesPossible bool      `json:"writesPossible"`
	KeepUntil      time.Time `json:"keepUntil"`
	Snapshot       string    `json:"snapshot"`
	Note           string    `json:"note"`
}
type View struct {
	Plan   Plan   `json:"plan"`
	Status Status `json:"status"`
}
type Command struct {
	Action       string              `json:"action"`
	Plan         Plan                `json:"plan"`
	Installation backup.Installation `json:"installation"`
	Secrets      map[string]string   `json:"secrets,omitempty"`
}

func (c Command) Digest() string {
	c.Installation.Access = backup.Access{}
	c.Installation.RepositoryID = ""
	c.Installation.Next = time.Time{}
	c.Secrets = nil
	return backup.Hash(c)
}
func (s Status) Active() bool {
	return s.Phase != "complete" && s.Phase != "returned" && s.Phase != "rejected" && s.Phase != "planned"
}
func (s Status) Allows(action string) bool {
	switch action {
	case "prepare":
		return s.Phase == "planned"
	case "cutover":
		return s.Phase == "ready" && !s.WritesPossible
	case "return":
		return s.Active() && !s.WritesPossible
	case "reconcile":
		return s.Active() && s.WritesPossible
	}
	return false
}
func (p Plan) Validate() error {
	if p.Recipe != Recipe || p.ID == uuid.Nil || p.PolicyID == uuid.Nil || p.ResourceID == uuid.Nil || p.TargetVolumeID == uuid.Nil || p.TargetPlanID == uuid.Nil || p.TargetRevisionID == uuid.Nil || p.RetainDays < 1 || p.RetainDays > 365 || p.ExpiresAt.IsZero() || p.Source.Validate(true) != nil || p.Source.Specification.Compose != nil {
		return errors.New("invalid upgrade plan")
	}
	if p.Group != nil && (p.Group.Validate(true) != nil || p.Group.Distributed == nil || p.Group.PrimaryPlan().Hash != p.Source.Hash) {
		return errors.New("upgrade group differs from its primary source")
	}
	if p.Source.Specification.HasRemotePlacement() && p.Group == nil {
		return errors.New("distributed upgrade requires the frozen source group")
	}
	count, found := 0, false
	for _, v := range p.Source.Specification.Volumes {
		if v.ID == p.TargetVolumeID {
			return errors.New("target volume must be new")
		}
	}
	for _, a := range p.Source.Specification.Applications {
		if a.Postgres != nil {
			count++
		}
		if a.ID == p.ResourceID && a.Postgres != nil && a.Source.Image != nil && *a.Source.Image == domain.Postgres17Image && deployment.ResolvedImage(domain.Postgres17Image, p.Source.Images[a.ID.String()]) {
			found = true
		}
	}
	if !found || count != 1 {
		return errors.New("recipe requires one managed PostgreSQL 17.10 database")
	}
	return nil
}
func (p Plan) OldVolume() uuid.UUID {
	for _, a := range p.Source.Specification.Applications {
		if a.ID == p.ResourceID && len(a.Mounts) == 1 {
			return a.Mounts[0].VolumeID
		}
	}
	return uuid.Nil
}
func (p Plan) Target() deployment.Plan {
	if p.Group != nil {
		return p.TargetGroup().PrimaryPlan()
	}
	return p.targetPlan(p.Source, p.TargetPlanID)
}

func (p Plan) TargetGroup() deployment.Plan {
	if p.Group == nil {
		return p.targetPlan(p.Source, p.TargetPlanID)
	}
	target := p.targetPlan(*p.Group, p.TargetPlanID)
	for index := range target.Distributed.Units {
		u := &target.Distributed.Units[index]
		source := p.Group.Distributed.Units[index].Plan
		u.Previous = source
		u.Plan = p.targetPlan(source, uuid.NewSHA1(p.TargetPlanID, []byte("unit:"+source.ServerID.String())))
		u.Plan.Local.GroupID = p.TargetPlanID
		u.Plan = u.Plan.WithHash()
	}
	return target.WithHash()
}

func (p Plan) targetPlan(source deployment.Plan, id uuid.UUID) deployment.Plan {
	raw, _ := json.Marshal(source)
	var target deployment.Plan
	_ = json.Unmarshal(raw, &target)
	target.ID, target.RevisionID = id, p.TargetRevisionID
	target.Restore = nil
	target.Release, target.ReleaseOptions = nil, nil
	target.ExpectedAppliedID = &p.Source.RevisionID
	target.ExpiresAt = p.ExpiresAt
	for i := range target.Specification.Applications {
		a := &target.Specification.Applications[i]
		if a.ID == p.ResourceID {
			image := domain.PostgresImage
			a.Source.Image = &image
			a.Mounts[0].VolumeID, a.Mounts[0].Target = p.TargetVolumeID, "/var/lib/postgresql"
			if target.Distributed == nil && target.Runs(a.ID) {
				target.Images[a.ID.String()] = image
			}
		}
	}
	for i := range target.Specification.Volumes {
		if target.Specification.Volumes[i].ID == p.OldVolume() {
			target.Specification.Volumes[i].ID = p.TargetVolumeID
		}
	}
	return target.WithHash()
}
