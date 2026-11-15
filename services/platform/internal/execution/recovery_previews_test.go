package execution

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

func previewRequestRow(id uuid.UUID, generation int64, env *uuid.UUID, state string) portability.Row {
	return appliedTestRow(map[string]any{"id": id, "generation": generation, "environment_id": env, "state": state})
}

// A request's generation advances when it is allocated an environment, which it holds until it is
// deleted and gives up only when it comes back, so the copy further along wins whichever side has it.
func TestRecoveryKeepsThePreviewRequestFurtherAlong(t *testing.T) {
	id, e, f := uuid.New(), uuid.New(), uuid.New()
	for name, change := range map[string]struct {
		prior, incoming portability.Row
		takesIncoming   bool
	}{
		"deleted in the checkpoint":     {previewRequestRow(id, 1, &e, "ready"), previewRequestRow(id, 1, &e, "deleted"), true},
		"deleted in the database":       {previewRequestRow(id, 1, &e, "deleted"), previewRequestRow(id, 1, &e, "ready"), false},
		"reopened in the checkpoint":    {previewRequestRow(id, 1, &e, "ready"), previewRequestRow(id, 1, nil, "pending"), true},
		"reopened in the database":      {previewRequestRow(id, 1, nil, "pending"), previewRequestRow(id, 1, &e, "deleted"), false},
		"reallocated in the checkpoint": {previewRequestRow(id, 1, &e, "deleted"), previewRequestRow(id, 2, &f, "ready"), true},
		"reallocated in the database":   {previewRequestRow(id, 2, &f, "ready"), previewRequestRow(id, 1, &e, "deleted"), false},
		"open on both sides":            {previewRequestRow(id, 1, &e, "blocked"), previewRequestRow(id, 1, &e, "ready"), false},
		"never allocated":               {previewRequestRow(id, 0, nil, "pending"), previewRequestRow(id, 0, nil, "deleted"), false},
	} {
		t.Run(name, func(t *testing.T) {
			selected, err := recoveryPreviewRequestKnowledge(change.prior, change.incoming)
			if err != nil {
				t.Fatal(err)
			}
			want := change.prior
			if change.takesIncoming {
				want = change.incoming
			}
			for key, value := range want {
				if string(selected[key]) != string(value) {
					t.Fatalf("%s = %s, want %s", key, selected[key], value)
				}
			}
		})
	}
}

// Of a policy's two copies, recovery keeps the one naming the host its open previews run on, so their
// cleanup goes where they are. Otherwise the copy with the higher generation wins, and on a tie the
// database's.
func TestRecoveryKeepsThePreviewPolicyCopyWhereItsPreviewsRun(t *testing.T) {
	policy, a, b := uuid.New(), uuid.New(), uuid.New()
	policyRow := func(server uuid.UUID, generation int64) portability.Row {
		return appliedTestRow(map[string]any{"id": policy, "server_id": server, "generation": generation})
	}
	for name, change := range map[string]struct {
		prior, checkpoint portability.Row
		running           []uuid.UUID
		takesCheckpoint   bool
	}{
		"moved, with the generations tied":        {policyRow(a, 2), policyRow(b, 2), []uuid.UUID{b}, true},
		"moved, with the database's copy higher":  {policyRow(a, 3), policyRow(b, 2), []uuid.UUID{b}, true},
		"not moved, with the checkpoint's higher": {policyRow(a, 2), policyRow(b, 3), []uuid.UUID{a}, false},
		"saved since, with no open previews":      {policyRow(a, 2), policyRow(b, 3), nil, true},
		"tied, with no open previews":             {policyRow(a, 2), policyRow(b, 2), nil, false},
		"open previews on both hosts":             {policyRow(a, 2), policyRow(b, 3), []uuid.UUID{a, b}, true},
		"same host, saved since":                  {policyRow(a, 2), policyRow(a, 3), []uuid.UUID{a}, true},
	} {
		t.Run(name, func(t *testing.T) {
			merged := portability.Archive{Tables: map[string][]portability.Row{"preview_policies": {change.prior}}}
			for _, server := range change.running {
				env := uuid.New()
				merged.Tables["deployment_units"] = append(merged.Tables["deployment_units"], appliedTestRow(map[string]any{"environment_id": env, "server_id": server}))
				merged.Tables["preview_requests"] = append(merged.Tables["preview_requests"], appliedTestRow(map[string]any{"policy_id": policy, "environment_id": env, "state": "ready"}))
			}
			// A deleted preview doesn't count, wherever it ran.
			env := uuid.New()
			merged.Tables["deployment_units"] = append(merged.Tables["deployment_units"], appliedTestRow(map[string]any{"environment_id": env, "server_id": a}))
			merged.Tables["preview_requests"] = append(merged.Tables["preview_requests"], appliedTestRow(map[string]any{"policy_id": policy, "environment_id": env, "state": "deleted"}))
			known, err := indexRecoveryRows(portability.Archive{Tables: map[string][]portability.Row{"preview_policies": {change.prior}}})
			if err != nil {
				t.Fatal(err)
			}
			incoming, err := indexRecoveryRows(portability.Archive{Tables: map[string][]portability.Row{"preview_policies": {change.checkpoint}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = settleRecoveredPreviewPolicies(merged, known, incoming); err != nil {
				t.Fatal(err)
			}
			want := change.prior
			if change.takesCheckpoint {
				want = change.checkpoint
			}
			for key, value := range want {
				if got := merged.Tables["preview_policies"][0][key]; string(got) != string(value) {
					t.Fatalf("%s = %s, want %s", key, got, value)
				}
			}
		})
	}
}
