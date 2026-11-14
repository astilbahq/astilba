package execution

import (
	"errors"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func candidate(secret uuid.UUID, recency int32, size int32, named bool) db.RuntimeSecretCheckCandidatesRow {
	row := db.RuntimeSecretCheckCandidatesRow{ID: uuid.New(), SecretID: secret, Recency: recency, Named: named}
	if size >= 0 {
		row.PlaintextBytes = pgtype.Int4{Int32: size, Valid: true}
	}
	return row
}

// rotated is one secret's newest versions, each of size bytes, as the candidates query lists them.
func rotated(versions int, size int32) []db.RuntimeSecretCheckCandidatesRow {
	secret := uuid.New()
	var out []db.RuntimeSecretCheckCandidatesRow
	for i := range versions {
		out = append(out, candidate(secret, int32(i+1), size, false))
	}
	return out
}

func chosenIDs(t *testing.T, candidates []db.RuntimeSecretCheckCandidatesRow) map[uuid.UUID]bool {
	t.Helper()
	chosen, err := chooseCheckedVersions(candidates)
	if err != nil {
		t.Fatal(err)
	}
	out := map[uuid.UUID]bool{}
	for _, c := range chosen {
		out[c.id] = c.required
	}
	return out
}

// Rotating a secret never pushes an environment past the budget: the versions it replaces are
// searched for only while they fit.
func TestRotationsNeverExceedTheSecretCheckBudget(t *testing.T) {
	var candidates []db.RuntimeSecretCheckCandidatesRow
	// Sixteen 64 KiB secrets, each rotated twice: 48 versions, three times the byte budget.
	for range 16 {
		candidates = append(candidates, rotated(secretCheckRecent, secretValueLimit)...)
	}
	chosen := chosenIDs(t, candidates)
	if len(chosen) != 16 {
		t.Fatalf("searched %d versions, want the 16 current ones", len(chosen))
	}
	for _, c := range candidates {
		if chosen[c.ID] != (c.Recency == 1) {
			t.Fatalf("version of recency %d: required %t", c.Recency, chosen[c.ID])
		}
	}

	// With room, a secret's last replaced version comes before any secret's older one.
	small := append(rotated(secretCheckRecent, 1000), rotated(secretCheckRecent, 1000)...)
	small = append(small, rotated(1, secretCheckBytes-4500)...)
	chosen = chosenIDs(t, small)
	for _, c := range small {
		if _, ok := chosen[c.ID]; ok != (c.Recency <= 2) {
			t.Fatalf("version of recency %d chosen: %t", c.Recency, ok)
		}
	}
}

// Versions the deployment may still use are searched for however old, and fail the check closed
// when they cannot all be.
func TestNamedVersionsAreRequired(t *testing.T) {
	secret := uuid.New()
	old := candidate(secret, 40, 100, true)
	chosen := chosenIDs(t, []db.RuntimeSecretCheckCandidatesRow{candidate(secret, 1, 100, false), old})
	if !chosen[old.ID] {
		t.Fatal("a named old version is not required")
	}

	var many []db.RuntimeSecretCheckCandidatesRow
	for range secretCheckVersions + 1 {
		many = append(many, candidate(uuid.New(), 1, 20, false))
	}
	if _, err := chooseCheckedVersions(many); !errors.Is(err, secretCheckBudget) {
		t.Fatalf("%d current versions: %v", len(many), err)
	}
	if _, err := chooseCheckedVersions(many[:secretCheckVersions]); err != nil {
		t.Fatal("exactly the version limit:", err)
	}
	large := []db.RuntimeSecretCheckCandidatesRow{candidate(uuid.New(), 1, secretCheckBytes, false), candidate(uuid.New(), 1, 1, true)}
	if _, err := chooseCheckedVersions(large); !errors.Is(err, secretCheckBudget) {
		t.Fatal("required versions over the byte budget:", err)
	}
}

// A version the measuring job has not reached counts at the most a value can hold when it is
// optional, so opening it can never cross the budget; a required one is measured when opened.
func TestUnmeasuredVersions(t *testing.T) {
	secret := uuid.New()
	current := candidate(secret, 1, secretCheckBytes-secretValueLimit+1, false)
	unmeasured := candidate(secret, 2, -1, false)
	chosen := chosenIDs(t, []db.RuntimeSecretCheckCandidatesRow{current, unmeasured})
	if _, ok := chosen[unmeasured.ID]; ok {
		t.Fatal("an unmeasured superseded version was chosen without room for any value")
	}
	required := candidate(uuid.New(), 1, -1, false)
	if chosen = chosenIDs(t, []db.RuntimeSecretCheckCandidatesRow{current, required}); !chosen[required.ID] {
		t.Fatal("an unmeasured current version is not required")
	}
}

// The answer kept for one search is never taken for another.
func TestSecretCheckKey(t *testing.T) {
	env, a, b := uuid.New(), uuid.New(), uuid.New()
	base := secretCheckKey(env, []string{"ab", "c"}, []checkedVersion{{id: a, required: true}})
	for name, other := range map[string][32]byte{
		"texts split differently": secretCheckKey(env, []string{"a", "bc"}, []checkedVersion{{id: a, required: true}}),
		"another environment":     secretCheckKey(uuid.New(), []string{"ab", "c"}, []checkedVersion{{id: a, required: true}}),
		"another version":         secretCheckKey(env, []string{"ab", "c"}, []checkedVersion{{id: b, required: true}}),
		"a version added":         secretCheckKey(env, []string{"ab", "c"}, []checkedVersion{{id: a, required: true}, {id: b}}),
		"optional":                secretCheckKey(env, []string{"ab", "c"}, []checkedVersion{{id: a}}),
	} {
		if other == base {
			t.Fatal(name, "shares a key")
		}
	}
}
