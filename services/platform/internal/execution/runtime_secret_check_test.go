package execution

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
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
	large := []db.RuntimeSecretCheckCandidatesRow{candidate(uuid.New(), 1, secretCheckBytes, false), candidate(uuid.New(), 1, 12, true)}
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

// A value too short to search for is never chosen, so it can neither match ordinary text nor
// take the budget.
func TestShortValuesAreNotSearched(t *testing.T) {
	short := candidate(uuid.New(), 1, 1, true)
	edge := candidate(uuid.New(), 1, 12, false)
	chosen := chosenIDs(t, []db.RuntimeSecretCheckCandidatesRow{short, candidate(uuid.New(), 1, 11, false), edge})
	if _, ok := chosen[short.ID]; ok || len(chosen) != 1 || !chosen[edge.ID] {
		t.Fatalf("chosen: %v", chosen)
	}
}

// BenchmarkSecretCheckWorstCase measures the search the bound allows on crafted input: the most
// versions or bytes the check opens, against text built to be slow to search. Run it with
// `go test -run '^$' -bench SecretCheckWorstCase ./internal/execution`.
func BenchmarkSecretCheckWorstCase(b *testing.B) {
	const textBytes = 1 << 20
	key := "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFB\n", 3) + "-----END PRIVATE KEY-----\n"
	texts := map[string]string{
		// Every byte starts a near match of the values below.
		"repeated letter": strings.Repeat("a", textBytes),
		// Each `%` and `+` adds a decoded reading; escapes and indentation change the line form.
		"encoded and escaped": strings.Repeat("%61+\\n  \\ta", textBytes/10),
		// A wrapped, commented base64 run that the bare form joins into one line.
		"wrapped base64": strings.Repeat("QUFBQUFBQUFB\n# ", textBytes/16),
		"key blocks":     strings.Repeat(key, textBytes/len(key)),
	}
	short := make([]string, secretCheckVersions)
	for i := range short {
		short[i] = strings.Repeat("a", 11) + string(rune('b'+i%20))
	}
	opaque := make([]string, secretCheckVersions)
	for i := range opaque {
		opaque[i] = strings.Repeat("QUFB", (secretCheckBytes/secretCheckVersions)/4-1) + fmt.Sprintf("%04d", i)
	}
	blocks := make([]string, secretCheckBytes/secretValueLimit)
	for i := range blocks {
		blocks[i] = strings.Repeat(key, secretValueLimit/len(key)-1) + fmt.Sprint(i)
	}
	values := map[string][]string{"256 versions of 12 bytes": short, "256 base64 versions, 1 MiB": opaque, "16 values of key blocks, 1 MiB": blocks}
	for textName, text := range texts {
		searched := domain.NewSecretText(text)
		for valueName, set := range values {
			b.Run(textName+"/"+valueName, func(b *testing.B) {
				for b.Loop() {
					for _, value := range set {
						searched.Exposes(value)
					}
				}
			})
		}
	}
}
