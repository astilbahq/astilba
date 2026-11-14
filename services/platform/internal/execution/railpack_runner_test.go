package execution

import (
	"errors"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
)

func advertising(pins pipeline.RailpackInputs) api.AgentCapabilities {
	version, digest := pins.Version, pins.Digest()
	return api.AgentCapabilities{RailpackVersion: &version, RailpackInputs: &digest}
}

// A release that moves a Railpack pin without a new Railpack version, as moving the helper
// image does.
func previousRelease(arch string) pipeline.RailpackInputs {
	pins := pipeline.RailpackFor(arch)
	pins.HelperImage = "docker.io/library/alpine:3.23@sha256:" + strings.Repeat("0", 64)
	return pins
}

func refusedForPins(t *testing.T, err error, need, advertised string) {
	t.Helper()
	var refusal *domain.Error
	if !errors.As(err, &refusal) || len(refusal.Fields) != 1 || refusal.Fields[0] != "runner.railpackUpgradeRequired" {
		t.Fatalf("not refused as a Railpack upgrade: %v", err)
	}
	if !strings.Contains(refusal.Message, need) || !strings.Contains(refusal.Message, advertised) {
		t.Fatalf("the refusal does not name both pin sets: %s", refusal.Message)
	}
}

// Management is upgraded before its agents: a runner still on the previous release runs
// other pins under the same Railpack version, so a new build is refused at admission
// rather than failing on the runner.
func TestRailpackRunnerOnThePreviousReleaseIsRefused(t *testing.T) {
	current := pipeline.RailpackFor("arm64")
	old := advertising(previousRelease("arm64"))
	refusedForPins(t, railpackRunner(old, current), current.Digest(), *old.RailpackInputs)
	// An agent from before the digest was advertised is refused too.
	version := current.Version
	refusedForPins(t, railpackRunner(api.AgentCapabilities{RailpackVersion: &version}, current), current.Digest(), "none")
	other := advertising(current)
	otherVersion := "0.42.0"
	other.RailpackVersion = &otherVersion
	refusedForPins(t, railpackRunner(other, current), current.Digest(), current.Digest())
	if err := railpackRunner(advertising(current), current); err != nil {
		t.Fatal("a runner on this release was refused", err)
	}
}

// Management is rolled back after its agents were upgraded: a build queued before the
// rollback froze the newer pins, and is delivered to the runners that run them. Delivery
// compares the build's frozen pins, not management's current ones.
func TestRailpackBuildFrozenBeforeARollbackStillReachesItsRunner(t *testing.T) {
	frozen := previousRelease("amd64") // pins this management no longer builds with
	if frozen.Digest() == pipeline.RailpackFor("amd64").Digest() {
		t.Fatal("fixture pins match the current release")
	}
	if err := railpackRunner(advertising(frozen), frozen); err != nil {
		t.Fatal("a runner on the build's frozen pins was refused", err)
	}
	current := advertising(pipeline.RailpackFor("amd64"))
	refusedForPins(t, railpackRunner(current, frozen), frozen.Digest(), *current.RailpackInputs)
}
