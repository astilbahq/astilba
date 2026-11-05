package portability

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
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
	altered.Tables["servers"][0]["preview_only"] = json.RawMessage("true")
	invalid, _ := json.Marshal(altered)
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
	raw, err = json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeArchive(raw); err != nil {
		t.Fatal("pre-scheduling archive rejected", err)
	}
	archive.Version = 7
	raw, _ = json.Marshal(archive)
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
	for _, row := range a.Tables["environments"] {
		delete(row, "runtime_operation_id")
	}
	for _, row := range a.Tables["execution_operations"] {
		delete(row, "runtime_outcome")
		delete(row, "runtime_plan_id")
	}
	raw, _ = json.Marshal(a)
	decoded, err := decodeArchive(raw)
	if err != nil || string(decoded.Tables["environments"][0]["runtime_operation_id"]) != "null" {
		t.Fatal("legacy provenance was inferred or rejected", err)
	}
	a.Tables["environments"][0]["runtime_operation_id"] = json.RawMessage("null")
	raw, _ = json.Marshal(a)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("version five accepted a new provenance field")
	}
	a.Version = 6
	for _, row := range a.Tables["execution_operations"] {
		row["runtime_outcome"] = json.RawMessage("null")
		row["runtime_plan_id"] = json.RawMessage("null")
	}
	raw, _ = json.Marshal(a)
	if _, err = decodeArchive(raw); err != nil {
		t.Fatal("version six rejected explicit unknown provenance", err)
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
