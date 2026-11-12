package deployment

import (
	"maps"
	"time"

	"github.com/google/uuid"
)

// Review is what reviewing and applying a plan needs: which plan and revision it is, what applying
// it does, the image digests it pins, the host permissions its Compose file asks for and any release
// command it runs. It leaves out the configuration the plan deploys (the specification, its plaintext
// variables and Compose files), so a caller that may plan or apply but not read the environment gets
// this instead of the plan. Fields are copied one by one, so a new Plan field reaches a Review only
// when it is added here.
type Review struct {
	ID          uuid.UUID         `json:"id"`
	ServerID    uuid.UUID         `json:"serverId"`
	RevisionID  uuid.UUID         `json:"revisionId"`
	Hash        string            `json:"hash"`
	ExpiresAt   time.Time         `json:"expiresAt"`
	HostChanges bool              `json:"hostChanges"`
	AcceptDrift bool              `json:"acceptDrift"`
	Effects     []string          `json:"effects"`
	Permissions []string          `json:"permissions"`
	Images      map[string]string `json:"images"`
	Release     *ReleaseReview    `json:"release"`
	// Units are the servers a cross-host plan changes, each with the images it pins there.
	Units []UnitReview `json:"units"`
}

type UnitReview struct {
	ServerID uuid.UUID         `json:"serverId"`
	Images   map[string]string `json:"images"`
	Release  *ReleaseReview    `json:"release"`
}

// ReleaseReview is the protected release a plan runs: the application it replaces and the command
// it runs first, with the environment's secrets.
type ReleaseReview struct {
	ApplicationID uuid.UUID      `json:"applicationId"`
	Options       ReleaseOptions `json:"options"`
}

func (p Plan) Review() (Review, error) {
	out := Review{ID: p.ID, ServerID: p.ServerID, RevisionID: p.RevisionID, Hash: p.Hash, ExpiresAt: p.ExpiresAt, HostChanges: p.HostChanges, AcceptDrift: p.AcceptDrift, Effects: append([]string{}, p.Effects...), Permissions: []string{}, Images: reviewImages(p.Images), Release: reviewRelease(p.Release), Units: []UnitReview{}}
	if p.Specification.Compose != nil {
		source, err := SourceProject(p, nil)
		if err != nil {
			return Review{}, err
		}
		out.Permissions = append(out.Permissions, source.Permissions...)
	}
	if p.Distributed != nil {
		for _, unit := range p.Distributed.Units {
			out.Units = append(out.Units, UnitReview{ServerID: unit.Plan.ServerID, Images: reviewImages(unit.Plan.Images), Release: reviewRelease(unit.Plan.Release)})
		}
	}
	return out, nil
}

func reviewImages(images map[string]string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, images)
	return out
}

func reviewRelease(r *Release) *ReleaseReview {
	if r == nil {
		return nil
	}
	options := r.Options
	options.Command = append([]string{}, r.Options.Command...)
	return &ReleaseReview{ApplicationID: r.ApplicationID, Options: options}
}
