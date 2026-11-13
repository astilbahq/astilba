package deployment

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A cross-host plan pins its images on each server's unit, not on the plan itself, so a review shows
// them by server: a credential that may not read the environment sees them nowhere else.
func TestCrossHostReviewShowsEachServersImages(t *testing.T) {
	app, first, second := uuid.New(), uuid.New(), uuid.New()
	image := func(c string) string { return "busybox@sha256:" + strings.Repeat(c, 64) }
	release := &Release{ApplicationID: app, PreviousPlanID: uuid.New(), Options: ReleaseOptions{Command: []string{"migrate"}, CommandTimeoutSeconds: 60, DrainSeconds: 10}}
	plan := Plan{ID: uuid.New(), ServerID: first, Images: map[string]string{}, Distributed: &DistributedPlan{Units: []DistributedUnit{
		{Plan: Plan{ServerID: first, Images: map[string]string{app.String(): image("a")}, Release: release}},
		{Plan: Plan{ServerID: second, Images: map[string]string{app.String(): image("b")}}},
	}}}
	review, err := plan.Review()
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Units) != 2 || review.Units[0].ServerID != first || review.Units[0].Images[app.String()] != image("a") || review.Units[1].ServerID != second || review.Units[1].Images[app.String()] != image("b") {
		t.Fatalf("the review lost a server's images: %+v", review.Units)
	}
	if r := review.Units[0].Release; r == nil || r.ApplicationID != app || len(r.Options.Command) != 1 || review.Units[1].Release != nil {
		t.Fatalf("the review lost a server's release: %+v", review.Units)
	}
}
