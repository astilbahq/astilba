package integration

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// storedSizes are a specification's canonical JSON, as ParseSpec measures it, and the size of its
// jsonb text, as specification_revisions' check measures it.
func storedSizes(t *testing.T, f *fixture, spec domain.Specification) (int, int) {
	t.Helper()
	raw, err := stablejson.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	_, canonical, err := domain.ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	size, err := f.st.Queries.SpecificationTextSize(t.Context(), canonical)
	if err != nil {
		t.Fatal(err)
	}
	return len(canonical), int(size)
}

// withDenseVariables adds the most variables a specification may have, whose separators make up
// much of its size, and pads their values so the specification's canonical JSON is short of the
// limit by margin.
func withDenseVariables(t *testing.T, f *fixture, spec domain.Specification, margin int) domain.Specification {
	t.Helper()
	spec.Variables = []domain.Variable{}
	for i := range 100 {
		value := "v"
		spec.Variables = append(spec.Variables, domain.Variable{Name: fmt.Sprintf("V%02d", i), Value: &value})
	}
	canonical, _ := storedSizes(t, f, spec)
	// A value may hold at most 4096 bytes.
	for i, pad := 0, domain.SpecificationLimit-margin-canonical; pad > 0; i++ {
		value := "v" + strings.Repeat("x", min(4095, pad))
		pad -= len(value) - 1
		spec.Variables[i].Value = &value
	}
	if canonical, _ = storedSizes(t, f, spec); canonical != domain.SpecificationLimit-margin {
		t.Fatalf("padded the specification to %d bytes", canonical)
	}
	return spec
}

// A specification within the limit as canonical JSON can be over it as jsonb text, which puts a space
// after every ':' and ','. A save is refused as invalid, not answered with the table's check failing,
// and the refusal leaves the transaction usable.
func TestSaveRefusesASpecificationOverTheLimitAsStored(t *testing.T) {
	f := setup(t)
	b, _, env, path := automationEnvironment(t, f)
	spec := withDenseVariables(t, f, domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}}, 100)
	if canonical, text := storedSizes(t, f, spec); canonical > domain.SpecificationLimit || text <= domain.SpecificationLimit {
		t.Fatalf("the specification is %d bytes canonically and %d as jsonb text", canonical, text)
	}
	var refused api.Error
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 400, &refused)
	invalidField(t, refused, "specification")

	_, canonical, err := domain.ParseSpec(claimBytes(t, spec))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.st.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	var ws uuid.UUID
	if err = tx.QueryRow(t.Context(), "SELECT workspace_id FROM environments WHERE id=$1", env.Id).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	var invalid *domain.Error
	if _, err = store.InsertRevision(t.Context(), db.New(tx), db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env.Id, ID: uuid.New(), AuthorID: uuid.New(), Specification: canonical, ContentHash: make([]byte, 32)}); !errors.As(err, &invalid) || invalid.Code != "invalid_input" {
		t.Fatalf("the insert answered %v", err)
	}
	if _, err = tx.Exec(t.Context(), "SELECT 1"); err != nil {
		t.Fatal("the refusal aborted the transaction:", err)
	}
	// The table's check stays the backstop, so it must measure the same limit.
	var check string
	if err = tx.QueryRow(t.Context(), "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='specification_revisions'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%octet_length%'").Scan(&check); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(check, fmt.Sprintf("octet_length((specification)::text) <= %d", domain.SpecificationLimit)) {
		t.Fatalf("the table's size check is %s, not domain.SpecificationLimit", check)
	}
}

// An import records why it failed in the transaction that refused its revision, so a revision over
// the limit as stored fails the import with a note rather than the runner's report with the table's
// check, which the runner would retry for ever.
func TestImportOverTheLimitAsStoredFails(t *testing.T) {
	i := newImportedEnvironment(t, "gitea")
	_, current := draftSpec(t, i.b, i.path)
	// The draft fits as stored, with room for the import to grow it only past the jsonb limit.
	canonical, text := storedSizes(t, i.f, withDenseVariables(t, i.f, current, 10000))
	overhead := text - canonical
	padded := withDenseVariables(t, i.f, current, overhead+10)
	i.b.call("PUT", i.path+"/draft", map[string]any{"baseRevisionId": i.draft.Id, "specification": padded}, 200, &i.draft)

	latest := strings.Repeat("e", 40)
	grown := []composeproject.File{importHonest[0], {Path: "config/app.env", Content: importHonest[1].Content + "# " + strings.Repeat("x", overhead/2) + "\n"}}
	imported := padded
	bundle := *padded.Compose
	bundle.Files = grown
	bundle.Origin = &composeproject.Origin{Provider: bundle.Origin.Provider, Repository: bundle.Origin.Repository, Commit: latest}
	imported.Compose = &bundle
	if canonical, text := storedSizes(t, i.f, imported); canonical > domain.SpecificationLimit || text <= domain.SpecificationLimit {
		t.Fatalf("the import would be %d bytes canonically and %d as jsonb text", canonical, text)
	}

	i.source.head.Store(latest)
	i.source.tree.record(latest, grown)
	c := i.refresh(t)
	i.runner.reportImport(c, latest, grown)
	if got := i.operation(t, c.Id); got.State != "failed" || got.Note != "Imported source is invalid for this environment." {
		t.Fatalf("the oversized import was %s: %q", got.State, got.Note)
	}
	if draft, _ := draftSpec(t, i.b, i.path); draft.Id != i.draft.Id {
		t.Fatal("the oversized import replaced the draft")
	}
}
