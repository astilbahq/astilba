package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// archivedVersions is the IDs of the secret versions an archive carries.
func archivedVersions(t *testing.T, a portability.Archive) []uuid.UUID {
	t.Helper()
	out := []uuid.UUID{}
	for _, row := range a.Tables["secret_versions"] {
		var id uuid.UUID
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return out
}

func sortedIDs(ids ...uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return slices.Compact(out)
}

// unresolvedReferences is every secret version a revision in ws names that the database lacks.
func unresolvedReferences(t *testing.T, f *fixture, ws uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := f.st.Pool.Query(context.Background(), `SELECT specification FROM specification_revisions WHERE workspace_id=$1`, ws)
	if err != nil {
		t.Fatal(err)
	}
	named := []uuid.UUID{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var spec domain.Specification
		if err := json.Unmarshal(raw, &spec); err != nil {
			t.Fatal(err)
		}
		named = append(named, domain.SecretReferences(spec)...)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	missing := []uuid.UUID{}
	for _, id := range sortedIDs(named...) {
		if !rowExists(t, f, `SELECT 1 FROM secret_versions WHERE workspace_id=$1 AND id=$2`, ws, id) {
			missing = append(missing, id)
		}
	}
	return missing
}

// Automation that sets a secret on every deploy used to grow every export and recovery base by a
// version each time. Of each secret they now carry its newest 10 versions and every version a
// carried row names: here the applied revision, the draft, a queued deployment and an operation's
// note, which stands for any shape a row may name one in. A base also carries every erased version,
// however old, so recovering from a backup that still holds the value erases it again. An import and
// a recovery from them leave no revision naming a missing version.
func TestArchivesCarryTheNewestAndNamedSecretVersions(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	b, ws, path := secretEnvironment(t, f)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var env api.Environment
	b.call("GET", path, nil, 200, &env)
	draftID := env.DraftRevisionId
	image := execution.TestImage
	app := uuid.New()
	saveDraft := func(version uuid.UUID) uuid.UUID {
		t.Helper()
		spec := domain.Specification{Version: 1, Variables: []domain.Variable{{Name: "TOKEN", SecretVersionID: &version}}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}}
		var draft api.Revision
		b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draftID, "specification": spec}, 200, &draft)
		draftID = draft.Id
		return draft.Id
	}

	// Versions 0 to 15 of one secret, each made by the API, so each has an audit event naming it.
	versions := make([]uuid.UUID, 16)
	var first api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "bounded-version-canary-0"}, 201, &first)
	versions[0] = first.Id
	for i := 1; i < len(versions); i++ {
		var v api.SecretVersion
		b.call("POST", path+"/secrets/"+first.SecretId.String()+"/versions", map[string]string{"value": fmt.Sprintf("bounded-version-canary-%d", i)}, 201, &v)
		versions[i] = v.Id
	}
	// A secret with fewer versions than the bound keeps them all.
	var other api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "OTHER", "value": "unbounded-other-canary"}, 201, &other)

	server, input, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	input.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	report := func(c api.ExecutionCommand, resolve bool) {
		t.Helper()
		observation := deployment.Observation{ObservedAt: time.Now().UTC(), Applied: !resolve, Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64)}
		if resolve {
			observation.Images = map[string]string{app.String(): image}
		} else {
			observation.Resources = []deployment.Resource{{ID: app, State: "running", Image: image}}
		}
		raw, _ := stablejson.Marshal(observation)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}, 204, nil)
	}
	// deploy plans the draft and applies it, leaving the deployment queued unless finish.
	deploy := func(revision uuid.UUID, finish bool) {
		t.Helper()
		var plan deployment.Status
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: server.Server.Id, BaseRevisionId: revision}, 201, &plan)
		var polled api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
		report(*polled.Command, true)
		b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, nil)
		if finish {
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
			report(*polled.Command, false)
		}
	}
	// The applied revision names version 0, a queued deployment version 3 and the draft version 1.
	applied := saveDraft(versions[0])
	deploy(applied, true)
	if !rowExists(t, f, `SELECT 1 FROM environments WHERE draft_revision_id=$1 AND applied_revision_id=$1`, applied) {
		t.Fatal("the first deployment did not apply")
	}
	deploy(saveDraft(versions[3]), false)
	if !rowExists(t, f, `SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND state='queued' AND command::text LIKE '%'||$2||'%'`, ws, versions[3].String()) {
		t.Fatal("no queued deployment names version 3")
	}
	saveDraft(versions[1])
	// Only the finished deployment's note names version 5.
	exec(`UPDATE execution_operations SET note='replaced '||$2 WHERE workspace_id=$1 AND state='succeeded' AND visual_plan_id IS NOT NULL`, ws, versions[5].String())
	if !rowExists(t, f, `SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND note LIKE '%'||$2`, ws, versions[5].String()) {
		t.Fatal("no operation's note names version 5")
	}
	// Version 2 is retired now and version 4 was erased long before any recovery window; a backup
	// from before then still holds their values.
	type value struct {
		sealed          []byte
		plaintext, json *int32
	}
	backup := map[uuid.UUID]value{}
	for _, id := range []uuid.UUID{versions[2], versions[4]} {
		var v value
		if err := f.st.Pool.QueryRow(ctx, `SELECT encrypted_value,plaintext_bytes,json_bytes FROM secret_versions WHERE id=$1`, id).Scan(&v.sealed, &v.plaintext, &v.json); err != nil {
			t.Fatal(err)
		}
		backup[id] = v
	}
	b.call("DELETE", path+"/secrets/"+first.SecretId.String()+"/versions/"+versions[2].String(), nil, 204, nil)
	exec(`UPDATE secret_versions SET encrypted_value=NULL,plaintext_bytes=NULL,json_bytes=NULL,erased_at=now()-interval '200 days' WHERE id=$1`, versions[4])

	// Versions 6 to 15 are the newest 10; 2, 4 and 6 to 15 are named only by audit events.
	newest := versions[len(versions)-portability.SecretVersionsKept:]
	newestFirst := slices.Clone(newest)
	slices.Reverse(newestFirst)
	if !slices.Equal(listedIDs(t, f, `SELECT id FROM secret_versions WHERE secret_id=$1 ORDER BY created_at DESC,id DESC LIMIT 10`, first.SecretId), newestFirst) {
		t.Fatal("the fixture's versions were not made in order")
	}
	exported := sortedIDs(append(slices.Clone(newest), versions[0], versions[1], versions[3], versions[5], other.Id)...)
	audited := func(a portability.Archive, id uuid.UUID) bool {
		for _, row := range a.Tables["audit_events"] {
			if string(row["resource_id"]) == `"`+id.String()+`"` {
				return true
			}
		}
		return false
	}

	// A recovery base carries the newest 10, the named versions and every erased one.
	base := capturedBase(t, f, ws)
	if got, want := archivedVersions(t, base), sortedIDs(append(slices.Clone(exported), versions[2], versions[4])...); !slices.Equal(got, want) {
		t.Fatalf("a recovery base carried versions %v, want %v", got, want)
	}

	// An export carries the newest 10 and the named versions, and its import names none it lacks.
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	const passphrase = "bounded secret versions passphrase"
	sealed, err := (&portability.Service{Store: f.st}).Export(ctx, browserPrincipal(t, f.st, account, true), ws, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := portability.Open(ctx, passphrase, sealed)
	if err != nil {
		t.Fatal(err)
	}
	var archive portability.Archive
	err = json.Unmarshal(plaintext, &archive)
	clear(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if got := archivedVersions(t, archive); !slices.Equal(got, exported) {
		t.Fatalf("an export carried versions %v, want %v", got, exported)
	}
	for _, id := range []uuid.UUID{versions[2], versions[4]} {
		if !audited(archive, id) {
			t.Fatal("the fixture no longer has an audit event naming a version the export leaves out")
		}
	}
	destination := setup(t)
	importer, err := destination.st.Queries.AccountByID(ctx, destination.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&portability.Service{Store: destination.st}).Import(ctx, browserPrincipal(t, destination.st, importer, true), passphrase, sealed, ""); err != nil {
		t.Fatal(err)
	}
	if got := sortedIDs(listedIDs(t, destination, `SELECT id FROM secret_versions WHERE workspace_id=$1`, ws)...); !slices.Equal(got, exported) {
		t.Fatalf("the import holds versions %v, want %v", got, exported)
	}
	if missing := unresolvedReferences(t, destination, ws); len(missing) != 0 {
		t.Fatal("an imported revision names a version the import lacks", missing)
	}
	if !rowExists(t, destination, `SELECT 1 FROM secret_versions WHERE id=$1 AND erased_at IS NULL`, versions[len(versions)-1]) {
		t.Fatal("the import lost the secret's current value")
	}

	// Recovery from a backup taken before versions 2 and 4 were erased and versions 9 to 15 were
	// made: the base brings back the newest versions and erases the values the backup still holds,
	// however long ago they were erased.
	for id, v := range backup {
		exec(`UPDATE secret_versions SET encrypted_value=$2,plaintext_bytes=$3,json_bytes=$4,erased_at=NULL WHERE id=$1`, id, v.sealed, v.plaintext, v.json)
	}
	exec(`DELETE FROM secret_versions WHERE id=ANY($1)`, versions[9:])
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = portability.MergeRecovery(ctx, tx, recoverKeys(t, f.st, ws), base, f.owner); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Merging again over its own result changes nothing.
	if err := mergeBase(t, f, ws, base); err != nil {
		t.Fatal("a second recovery from the base:", err)
	}
	if missing := unresolvedReferences(t, f, ws); len(missing) != 0 {
		t.Fatal("a recovered revision names a missing version", missing)
	}
	for _, id := range versions[9:] {
		if !rowExists(t, f, `SELECT 1 FROM secret_versions WHERE id=$1 AND erased_at IS NULL AND encrypted_value IS NOT NULL`, id) {
			t.Fatal("recovery lost a version the base carries")
		}
	}
	for id := range backup {
		if !rowExists(t, f, `SELECT 1 FROM secret_versions WHERE id=$1 AND erased_at IS NOT NULL AND encrypted_value IS NULL`, id) {
			t.Fatal("recovery brought back an erased value")
		}
	}
	// The list shows each secret's live versions, newest first.
	var listed []api.SecretVersion
	b.call("GET", path+"/secrets", nil, 200, &listed)
	i := slices.IndexFunc(listed, func(v api.SecretVersion) bool { return v.SecretId == first.SecretId })
	if i < 0 {
		t.Fatal("after recovery the secret isn't listed")
	}
	current := listed[i]
	if current.Id != versions[len(versions)-1] {
		t.Fatal("after recovery the secret's current version is", current.Id)
	}
}
