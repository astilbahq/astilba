package portability

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func TestLegacyArchivePurposeAndStrictShape(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := decodeArchive(raw)
	if err != nil {
		t.Fatal("pre-preview archive rejected", err)
	}
	if string(archive.Tables["environments"][0]["purpose"]) != `"standard"` || string(archive.Tables["servers"][0]["preview_only"]) != "false" {
		t.Fatal("legacy purpose changed")
	}
	// Legacy normalization must not turn an unknown grant field into a default.
	var altered Archive
	if err = json.Unmarshal(raw, &altered); err != nil {
		t.Fatal(err)
	}
	altered.Tables["servers"][0]["preview_only"] = jsontext.Value("true")
	invalid, _ := stablejson.Marshal(altered)
	if _, err = decodeArchive(invalid); err == nil {
		t.Fatal("accepted a new authority field in a legacy archive")
	}
}

func TestPreSchedulingVersionTwoArchivesRemainReadable(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	archive.Version = 2
	for _, table := range tables {
		if strings.HasPrefix(table.Name, "network_") || table.Name == "workspace_networks" || strings.HasPrefix(table.Name, "cross_host_") || table.Name == "backup_cutovers" {
			delete(archive.Tables, table.Name)
		}
	}
	for _, row := range archive.Tables["deployment_units"] {
		delete(row, "primary_unit")
	}
	for _, row := range archive.Tables["execution_operations"] {
		delete(row, "parent_id")
	}
	for _, row := range archive.Tables["environments"] {
		delete(row, "runtime_operation_id")
	}
	for _, row := range archive.Tables["execution_operations"] {
		delete(row, "runtime_outcome")
		delete(row, "runtime_plan_id")
	}
	withoutVersionEight(archive)
	delete(archive.Tables, "source_submissions")
	raw, err = stablejson.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeArchive(raw); err != nil {
		t.Fatal("pre-scheduling archive rejected", err)
	}
	archive.Version = ArchiveVersion + 1
	raw, _ = stablejson.Marshal(archive)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("unknown future schedule semantics accepted")
	}
}

func TestVersionFivePreservesUnknownAppliedProvenance(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.Version = 5
	withoutVersionEight(a)
	delete(a.Tables, "source_submissions")
	for _, row := range a.Tables["environments"] {
		delete(row, "runtime_operation_id")
	}
	for _, row := range a.Tables["execution_operations"] {
		delete(row, "runtime_outcome")
		delete(row, "runtime_plan_id")
	}
	raw, _ = stablejson.Marshal(a)
	decoded, err := decodeArchive(raw)
	if err != nil || string(decoded.Tables["environments"][0]["runtime_operation_id"]) != "null" {
		t.Fatal("legacy provenance was inferred or rejected", err)
	}
	a.Tables["environments"][0]["runtime_operation_id"] = jsontext.Value("null")
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("version five accepted a new provenance field")
	}
	a.Version = 6
	for _, row := range a.Tables["execution_operations"] {
		row["runtime_outcome"] = jsontext.Value("null")
		row["runtime_plan_id"] = jsontext.Value("null")
	}
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err != nil {
		t.Fatal("version six rejected explicit unknown provenance", err)
	}
	a.Tables["source_submissions"] = []Row{}
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("version six accepted management submissions")
	}
	a.Version = 7
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err != nil {
		t.Fatal("version seven rejected its submissions", err)
	}
}

// withoutVersionNine makes a decoded archive look as one written before purges and retired secrets.
func withoutVersionNine(a Archive) {
	withoutVersionEleven(a)
	for _, row := range a.Tables["environments"] {
		for _, field := range []string{"purged_at", "purged_by", "purge_operation_id", "purged_without_host"} {
			delete(row, field)
		}
	}
	for _, row := range a.Tables["secrets"] {
		delete(row, "retired_at")
	}
	for _, row := range a.Tables["secret_versions"] {
		delete(row, "erased_at")
	}
}

// withoutVersionEleven makes a decoded archive look as one written before it recorded which preview
// requests revocation released.
func withoutVersionEleven(a Archive) {
	withoutVersionThirteen(a)
	for _, row := range a.Tables["preview_requests"] {
		delete(row, "released_by_revocation")
	}
}

// previewRequestRow is a preview request revocation released, with every column the current format
// carries.
func previewRequestRow(t *testing.T, a Archive) Row {
	t.Helper()
	row := Row{}
	for _, table := range tables {
		if table.Name == "preview_requests" {
			for _, column := range strings.Split(table.Columns, ",") {
				row[column] = jsontext.Value("null")
			}
		}
	}
	ws, _ := stablejson.Marshal(a.Workspace)
	row["workspace_id"] = ws
	row["id"] = jsontext.Value(`"` + uuid.NewString() + `"`)
	row["policy_id"] = jsontext.Value(`"` + uuid.NewString() + `"`)
	row["state"] = jsontext.Value(`"deleted"`)
	row["released_by_revocation"] = jsontext.Value("true")
	return row
}

// An archive records which preview requests revoking their host released, so a request that came
// back keeps saying it's a new preview. One from before version 11 records none, and may not carry
// the field itself.
func TestArchiveCarriesPreviewsRevocationReleased(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(version int, released bool) (Archive, error) {
		t.Helper()
		row := previewRequestRow(t, a)
		if !released {
			delete(row, "released_by_revocation")
		}
		older := a
		older.Version, older.Tables = version, maps.Clone(a.Tables)
		if version < 13 {
			withoutVersionThirteen(older)
		}
		older.Tables["preview_requests"] = []Row{row}
		raw, err := stablejson.Marshal(older)
		if err != nil {
			t.Fatal(err)
		}
		return decodeArchive(raw)
	}
	decoded, err := decode(ArchiveVersion, true)
	if err != nil || string(decoded.Tables["preview_requests"][0]["released_by_revocation"]) != "true" {
		t.Fatal("the current version lost a release by revocation", err)
	}
	if _, err = decode(ArchiveVersion, false); err == nil {
		t.Fatal("the current version accepted a request without the field")
	}
	decoded, err = decode(9, false)
	if err != nil || string(decoded.Tables["preview_requests"][0]["released_by_revocation"]) != "false" {
		t.Fatal("a version nine request was imported released by revocation, or refused", err)
	}
	if _, err = decode(9, true); err == nil {
		t.Fatal("version nine accepted the field")
	}
}

// withoutVersionEight makes a decoded archive look as one written before retirement.
func withoutVersionEight(a Archive) {
	withoutVersionNine(a)
	for _, row := range a.Tables["environments"] {
		for _, field := range []string{"state", "retired_at", "retired_by", "retirement_operation_id", "retired_without_host"} {
			delete(row, field)
		}
	}
	for _, row := range a.Tables["managed_volumes"] {
		delete(row, "postgres_major")
	}
}

// An archive from before retirement imports every environment as active, with no retirement and no
// recorded PostgreSQL major, and may not carry the newer fields itself.
func TestVersionSevenArchiveImportsActiveEnvironments(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.Version = 7
	withoutVersionEight(a)
	raw, _ = stablejson.Marshal(a)
	decoded, err := decodeArchive(raw)
	if err != nil {
		t.Fatal("version seven rejected", err)
	}
	env := decoded.Tables["environments"][0]
	if string(env["state"]) != `"active"` || string(env["retired_at"]) != "null" || string(env["retired_by"]) != "null" || string(env["retirement_operation_id"]) != "null" || string(env["retired_without_host"]) != "false" {
		t.Fatal("a version seven environment was not imported as active", env)
	}
	a.Tables["environments"][0]["state"] = jsontext.Value(`"retired"`)
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("version seven accepted a retirement field")
	}
}

// An archive from before purges imports nothing purged, retired or erased, and may not carry the
// newer fields itself.
func TestVersionEightArchiveImportsNothingPurged(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.Version = 8
	withoutVersionNine(a)
	raw, _ = stablejson.Marshal(a)
	decoded, err := decodeArchive(raw)
	if err != nil {
		t.Fatal("version eight rejected", err)
	}
	env := decoded.Tables["environments"][0]
	if string(env["purged_at"]) != "null" || string(env["purged_by"]) != "null" || string(env["purge_operation_id"]) != "null" || string(env["purged_without_host"]) != "false" {
		t.Fatal("a version eight environment was imported purged", env)
	}
	a.Tables["environments"][0]["purged_without_host"] = jsontext.Value("false")
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("version eight accepted a purge field")
	}
}

// An erased secret version holds nothing to open or seal, and a row whose value and erasure
// disagree is not one an export writes.
func TestErasedSecretVersionsCarryNoValue(t *testing.T) {
	id := jsontext.Value(`"` + uuid.NewString() + `"`)
	secret := uuid.New()
	environments := secretEnvironments{secret: uuid.New()}
	for _, c := range []struct {
		name          string
		value, erased string
		field         string
		fails         bool
	}{
		{"kept", `"\x00"`, "null", "encrypted_value", false},
		{"erased", "null", `"2026-11-13T00:00:00Z"`, "", false},
		{"value without erasure", "null", "null", "", true},
		{"erasure with a value", `"\x00"`, `"2026-11-13T00:00:00Z"`, "", true},
	} {
		field, _, err := credential("secret_versions", Row{"id": id, "secret_id": jsontext.Value(`"` + secret.String() + `"`), "encrypted_value": jsontext.Value(c.value), "erased_at": jsontext.Value(c.erased)}, environments)
		if (err != nil) != c.fails || !c.fails && field != c.field {
			t.Errorf("%s: field %q, error %v", c.name, field, err)
		}
	}
}

// A retirement its server confirmed released the environment's hostnames and ports, so an archive
// that still has it claiming one is refused rather than taking them back.
func TestArchiveRefusesClaimsOfAConfirmedRetirement(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.Version = ArchiveVersion
	env := a.Tables["environments"][0]
	a.Tables["route_claims"] = append(a.Tables["route_claims"], Row{"server_id": a.Tables["servers"][0]["id"], "claim": jsontext.Value(`"http:retired.example.test"`), "workspace_id": env["workspace_id"], "environment_id": env["id"]})
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err != nil {
		t.Fatal("an active environment's claim was refused", err)
	}
	env["state"], env["retired_at"], env["retired_by"] = jsontext.Value(`"retired"`), jsontext.Value(`"2026-01-01T00:00:00Z"`), env["id"]
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("a released claim was imported")
	}
	// A purge follows the retirement that released them.
	for _, state := range []string{`"purging"`, `"purged"`} {
		env["state"] = jsontext.Value(state)
		raw, _ = stablejson.Marshal(a)
		if _, err = decodeArchive(raw); err == nil {
			t.Fatal("a purged environment's released claim was imported", state)
		}
	}
	env["state"] = jsontext.Value(`"retired"`)
	// A server that could not be reached may still serve them, so they stay with it.
	env["retired_without_host"] = jsontext.Value("true")
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err != nil {
		t.Fatal("an unreachable server's claim was refused", err)
	}
}

// Archives are operator-supplied files. Names that mean nothing to PostgreSQL's
// exporter must not gain meaning through case folding or last-duplicate-wins.
func TestArchiveRefusesAmbiguousNames(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, altered := range map[string]string{
		"case variant": strings.Replace(string(raw), `"name"`, `"NAME"`, 1),
		"duplicate":    strings.Replace(string(raw), `"name"`, `"name":"other","name"`, 1),
	} {
		if altered == string(raw) {
			t.Fatal("fixture no longer has a name")
		}
		if _, err = decodeArchive([]byte(altered)); err == nil {
			t.Fatalf("accepted a %s", name)
		}
	}
}

// withoutVersionThirteen makes a decoded archive look as one written before it recorded when
// revocations began and when snapshots were first reported. It copies the rows it changes.
func withoutVersionThirteen(a Archive) {
	withoutVersionFifteen(a)
	for table, column := range map[string]string{"servers": "revoked_at", "backup_policies": "snapshot_receipts"} {
		rows := []Row{}
		for _, row := range a.Tables[table] {
			row = maps.Clone(row)
			delete(row, column)
			rows = append(rows, row)
		}
		a.Tables[table] = rows
	}
}

// An archive carries when each server's revocation began and when management first received each
// snapshot. One from before version 13 records neither and may not carry them, and an archive's
// receipts must be ones its policy's reports could have made.
func TestArchiveCarriesRevocationTimesAndSnapshotReceipts(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	server := a.Tables["servers"][0]
	if string(server["revoked_at"]) != "null" {
		t.Fatal("an archive from before version 13 was given a revocation time")
	}
	receipts := func(entries int) jsontext.Value {
		out := map[string]string{}
		for i := range entries {
			out[fmt.Sprintf("%064x", i)] = "2026-11-01T00:00:00Z"
		}
		raw, _ := stablejson.Marshal(out)
		return raw
	}
	policy := Row{"id": jsontext.Value(`"` + uuid.NewString() + `"`), "workspace_id": server["workspace_id"], "environment_id": a.Tables["environments"][0]["id"], "server_id": server["id"], "configuration": jsontext.Value(`{"serverId":` + string(server["id"]) + `}`), "encrypted_access": jsontext.Value(`null`), "status": jsontext.Value(`{}`), "status_sequence": jsontext.Value(`0`), "snapshot_receipts": receipts(2)}
	decode := func(version int, change func(Archive)) (Archive, error) {
		t.Helper()
		older := a
		older.Version, older.Tables = version, maps.Clone(a.Tables)
		older.Tables["servers"] = []Row{maps.Clone(server)}
		older.Tables["servers"][0]["revoked_at"] = jsontext.Value(`"2026-11-02T00:00:00Z"`)
		older.Tables["backup_policies"] = []Row{maps.Clone(policy)}
		if version < 13 {
			withoutVersionThirteen(older)
		}
		change(older)
		raw, err := stablejson.Marshal(older)
		if err != nil {
			t.Fatal(err)
		}
		return decodeArchive(raw)
	}
	got, err := decode(ArchiveVersion, func(Archive) {})
	if err != nil || string(got.Tables["servers"][0]["revoked_at"]) != `"2026-11-02T00:00:00Z"` || string(got.Tables["backup_policies"][0]["snapshot_receipts"]) != string(receipts(2)) {
		t.Fatal("the current version didn't carry a revocation time and receipts", err)
	}
	if got, err = decode(11, func(Archive) {}); err != nil || string(got.Tables["servers"][0]["revoked_at"]) != "null" || string(got.Tables["backup_policies"][0]["snapshot_receipts"]) != "{}" {
		t.Fatal("version 11 wasn't read as recording neither", err)
	}
	set := func(table, column, value string) func(Archive) {
		return func(a Archive) { a.Tables[table][0][column] = jsontext.Value(value) }
	}
	for name, change := range map[string]func(Archive){
		"a policy without receipts":         func(a Archive) { delete(a.Tables["backup_policies"][0], "snapshot_receipts") },
		"null receipts":                     set("backup_policies", "snapshot_receipts", `null`),
		"receipts that aren't a map":        set("backup_policies", "snapshot_receipts", `[]`),
		"a receipt for no snapshot":         set("backup_policies", "snapshot_receipts", `{"latest":"2026-11-01T00:00:00Z"}`),
		"a receipt without a time":          set("backup_policies", "snapshot_receipts", `{"`+strings.Repeat("a", 64)+`":"yesterday"}`),
		"more receipts than reports list":   func(a Archive) { a.Tables["backup_policies"][0]["snapshot_receipts"] = receipts(129) },
		"version 11 with a revocation time": nil,
		"version 11 with receipts":          nil,
	} {
		version := ArchiveVersion
		switch name {
		case "version 11 with a revocation time":
			version, change = 11, set("servers", "revoked_at", `null`)
		case "version 11 with receipts":
			version, change = 11, set("backup_policies", "snapshot_receipts", `{}`)
		}
		if _, err := decode(version, change); err == nil {
			t.Error("an archive with", name, "was accepted")
		}
	}
}

// withoutVersionFifteen makes a decoded archive look as one written before it carried restores onto
// another server.
func withoutVersionFifteen(a Archive) {
	withoutVersionNineteen(a)
	delete(a.Tables, "backup_recoveries")
}

// withoutVersionNineteen makes a decoded archive look as one written before it carried the server
// each backup destination is bound to.
func withoutVersionNineteen(a Archive) {
	withoutVersionTwentyOne(a)
	rows := []Row{}
	for _, row := range a.Tables["backup_destinations"] {
		row = maps.Clone(row)
		delete(row, "server_id")
		delete(row, "shared")
		rows = append(rows, row)
	}
	a.Tables["backup_destinations"] = rows
}

// An archive from before version 15 made no restore onto another server, and may not carry one.
func TestVersionThirteenArchivesCarryNoRecoveries(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.Version = 13
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("a version 13 archive carried restores onto another server")
	}
	withoutVersionFifteen(a)
	raw, _ = stablejson.Marshal(a)
	got, err := decodeArchive(raw)
	if err != nil || got.Tables["backup_recoveries"] == nil || len(got.Tables["backup_recoveries"]) != 0 {
		t.Fatal("a version 13 archive wasn't read as making no restores: ", err)
	}
}

// An imported restore result must be one its own restore could have recorded, or listing the
// policy's restores would fail on it.
func TestImportedRestoreResultsMatchTheirRestore(t *testing.T) {
	policy, environment, target, snapshot := uuid.New(), uuid.New(), uuid.New(), strings.Repeat("e", 64)
	row := func(change func(*backup.RecoveryResult)) Row {
		r := backup.RecoveryResult{Snapshot: snapshot, TargetID: target, Manifest: backup.Manifest{Recipe: backup.Recipe, PolicyID: policy, RunID: uuid.New(), EnvironmentID: environment, ResourceID: uuid.New(), Method: backup.QuiescedFiles, Bytes: 4096, SHA256: strings.Repeat("d", 64), Roles: []backup.Role{}, Extensions: []string{}}}
		result, _ := stablejson.Marshal(r)
		if change != nil {
			change(&r)
			result, _ = stablejson.Marshal(r)
		}
		quoted := func(v string) jsontext.Value { return jsontext.Value(`"` + v + `"`) }
		return Row{"policy_id": quoted(policy.String()), "environment_id": quoted(environment.String()), "target_volume_id": quoted(target.String()), "snapshot": quoted(snapshot), "result": result}
	}
	if err := checkRecoveryResult(row(nil)); err != nil {
		t.Fatal("a restore's own result was refused:", err)
	}
	unrecorded := row(nil)
	unrecorded["result"] = jsontext.Value(`null`)
	if err := checkRecoveryResult(unrecorded); err != nil {
		t.Fatal("a restore without a result was refused:", err)
	}
	for name, change := range map[string]func(*backup.RecoveryResult){
		"another snapshot":         func(r *backup.RecoveryResult) { r.Snapshot = strings.Repeat("f", 64) },
		"another target":           func(r *backup.RecoveryResult) { r.TargetID = uuid.New() },
		"another policy":           func(r *backup.RecoveryResult) { r.Manifest.PolicyID = uuid.New() },
		"another environment":      func(r *backup.RecoveryResult) { r.Manifest.EnvironmentID = uuid.New() },
		"another recipe":           func(r *backup.RecoveryResult) { r.Manifest.Recipe = backup.Recipe + 1 },
		"no archive digest":        func(r *backup.RecoveryResult) { r.Manifest.SHA256 = "" },
		"a manifest without roles": func(r *backup.RecoveryResult) { r.Manifest.Roles = nil },
	} {
		if checkRecoveryResult(row(change)) == nil {
			t.Error("a result with", name, "was accepted")
		}
	}
	for _, value := range []string{`[]`, `5`, `{"manifest":1}`, `{"manifest":{"bytes":"x"}}`} {
		r := row(nil)
		r["result"] = jsontext.Value(value)
		if checkRecoveryResult(r) == nil {
			t.Error("a result of", value, "was accepted")
		}
	}
}

// withoutVersionTwentyOne makes a decoded archive look as one written before managed storage keys.
func withoutVersionTwentyOne(a Archive) {
	delete(a.Tables, "backup_storage_accounts")
	delete(a.Tables, "backup_storage_keys")
	for table, column := range map[string]string{"backup_destinations": "account_id", "servers": "revocations"} {
		rows := []Row{}
		for _, row := range a.Tables[table] {
			row = maps.Clone(row)
			delete(row, column)
			rows = append(rows, row)
		}
		a.Tables[table] = rows
	}
}
