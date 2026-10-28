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
	raw, err = json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeArchive(raw); err != nil {
		t.Fatal("pre-scheduling archive rejected", err)
	}
	archive.Version = 6
	raw, _ = json.Marshal(archive)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("unknown future schedule semantics accepted")
	}
}
