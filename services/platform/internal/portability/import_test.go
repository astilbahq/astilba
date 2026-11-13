package portability

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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

// withoutVersionEight makes a decoded archive look as one written before retirement.
func withoutVersionEight(a Archive) {
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
