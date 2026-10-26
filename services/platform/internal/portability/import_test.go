package portability

import (
	"encoding/json"
	"os"
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
	raw, err = json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeArchive(raw); err != nil {
		t.Fatal("pre-scheduling archive rejected", err)
	}
	archive.Version = 5
	raw, _ = json.Marshal(archive)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("unknown future schedule semantics accepted")
	}
}
