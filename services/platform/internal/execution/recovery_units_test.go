package execution

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

func unitRow(t *testing.T, server *uuid.UUID, primary bool) portability.Row {
	t.Helper()
	return appliedTestRow(map[string]any{"server_id": server, "primary_unit": primary})
}

// Replacing a revoked primary promotes one unit and detaches another. Either archive can be the
// newer, because a capsule is frozen when its root starts, so the unit's furthest stage wins.
func TestRecoveryKeepsAUnitsFurthestStage(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	for name, change := range map[string]struct {
		prior, incoming, want portability.Row
		ok                    bool
	}{
		"gains a server":             {unitRow(t, nil, true), unitRow(t, &a, true), unitRow(t, &a, true), true},
		"unchanged":                  {unitRow(t, &a, false), unitRow(t, &a, false), unitRow(t, &a, false), true},
		"replaced since the backup":  {unitRow(t, &a, true), unitRow(t, nil, false), unitRow(t, nil, false), true},
		"promoted since the backup":  {unitRow(t, &b, false), unitRow(t, &b, true), unitRow(t, &b, true), true},
		"replaced before the backup": {unitRow(t, nil, false), unitRow(t, &a, true), unitRow(t, nil, false), true},
		"moved to another host":      {unitRow(t, &a, true), unitRow(t, &b, true), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			selected := portability.Row{}
			for key, value := range change.prior {
				selected[key] = value
			}
			err := recoveryUnitKnowledge(selected, change.prior, change.incoming)
			if (err == nil) != change.ok {
				t.Fatalf("merge error %v, want ok %v", err, change.ok)
			}
			for key, value := range change.want {
				if string(selected[key]) != string(value) {
					t.Fatalf("%s = %s, want %s", key, selected[key], value)
				}
			}
		})
	}
}

// A checkpoint's base carries no servers, so a replacement's detached unit is recognised from the
// revocations the other recovery inputs hold.
func TestRecoveryProjectionKeepsADetachedPrimary(t *testing.T) {
	a, p := appliedTestArchive(t)
	a.Tables["deployment_units"] = []portability.Row{appliedTestRow(map[string]any{"id": p.UnitID, "environment_id": p.EnvironmentID, "server_id": nil, "primary_unit": false})}
	environment := a.Tables["environments"][0]
	if _, err := recoveryProjectionFrom(a, p.EnvironmentID, environment, map[uuid.UUID]bool{}); err == nil {
		t.Fatal("a detached unit on a healthy server was accepted")
	}
	projection, err := recoveryProjectionFrom(a, p.EnvironmentID, environment, map[uuid.UUID]bool{p.ServerID: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.units) != 0 {
		t.Fatalf("the detached unit was bound again: %v", projection.units)
	}
}
