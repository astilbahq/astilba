package portability

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"os"
	"strings"
	"testing"

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
		field, _, err := credential("secret_versions", Row{"id": id, "encrypted_value": jsontext.Value(c.value), "erased_at": jsontext.Value(c.erased)})
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
	servers := []Row{}
	for _, row := range a.Tables["servers"] {
		row = maps.Clone(row)
		delete(row, "revoked_at")
		servers = append(servers, row)
	}
	a.Tables["servers"] = servers
	delete(a.Tables, "backup_snapshot_receipts")
}

// An archive carries when each server's revocation began and when management first received each
// snapshot. One from before version 13 records neither and may not carry them, and a receipt must
// name a policy and a server the archive holds.
func TestArchiveCarriesRevocationTimesAndSnapshotReceipts(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Tables["servers"][0]["revoked_at"]) != "null" || len(a.Tables["backup_snapshot_receipts"]) != 0 {
		t.Fatal("an archive from before version 13 was given a revocation time or a receipt")
	}
	server := a.Tables["servers"][0]
	receipt := Row{"workspace_id": server["workspace_id"], "environment_id": jsontext.Value(`"` + uuid.NewString() + `"`), "policy_id": jsontext.Value(`"` + uuid.NewString() + `"`), "snapshot": jsontext.Value(`"` + strings.Repeat("a", 64) + `"`), "server_id": server["id"], "first_reported_at": jsontext.Value(`"2026-11-01T00:00:00Z"`)}
	decode := func(version int, change func(Archive)) error {
		t.Helper()
		older := a
		older.Version, older.Tables = version, maps.Clone(a.Tables)
		older.Tables["servers"] = []Row{maps.Clone(server)}
		older.Tables["servers"][0]["revoked_at"] = jsontext.Value(`"2026-11-02T00:00:00Z"`)
		if version < 13 {
			withoutVersionThirteen(older)
		}
		change(older)
		raw, err := stablejson.Marshal(older)
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeArchive(raw)
		return err
	}
	if err = decode(ArchiveVersion, func(Archive) {}); err != nil {
		t.Fatal("the current version refused a revocation time", err)
	}
	if decode(ArchiveVersion, func(a Archive) { delete(a.Tables, "backup_snapshot_receipts") }) == nil {
		t.Fatal("the current version accepted an archive without receipts")
	}
	if decode(ArchiveVersion, func(a Archive) { a.Tables["backup_snapshot_receipts"] = []Row{receipt} }) == nil {
		t.Fatal("a receipt for a policy the archive doesn't hold was accepted")
	}
	if err = decode(11, func(Archive) {}); err != nil {
		t.Fatal("version 11 was refused", err)
	}
	if decode(11, func(a Archive) { a.Tables["backup_snapshot_receipts"] = []Row{} }) == nil {
		t.Fatal("version 11 accepted a receipts table")
	}
	if decode(11, func(a Archive) { a.Tables["servers"][0]["revoked_at"] = jsontext.Value("null") }) == nil {
		t.Fatal("version 11 accepted a revocation time")
	}
}
