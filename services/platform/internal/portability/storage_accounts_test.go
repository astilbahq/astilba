package portability

import (
	"encoding/json/v2"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// An import lists the exporting installation as a peer of each storage account, never itself, and
// keeps to the database's limit.
func TestAnImportListsTheExporterAsAPeer(t *testing.T) {
	self, exporter, other := uuid.New(), uuid.New(), uuid.New()
	peers := func(row Row) []uuid.UUID {
		var out []uuid.UUID
		if err := json.Unmarshal(row["peers"], &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	row := Row{}
	setValue(row, "peers", []uuid.UUID{other, self})
	if err := importPeer(row, exporter, self); err != nil {
		t.Fatal(err)
	}
	if got := peers(row); !slices.Equal(got, []uuid.UUID{other, exporter}) {
		t.Fatalf("peers = %v", got)
	}
	if err := importPeer(row, exporter, self); err != nil || len(peers(row)) != 2 {
		t.Fatalf("a second import listed the exporter twice: %v", peers(row))
	}
	full := make([]uuid.UUID, maxStoragePeers)
	for i := range full {
		full[i] = uuid.New()
	}
	setValue(row, "peers", full)
	if err := importPeer(row, exporter, self); err != nil || slices.Contains(peers(row), exporter) {
		t.Fatal("a full list took another peer")
	}
	setValue(row, "peers", append(full, uuid.New()))
	if importPeer(row, exporter, self) == nil {
		t.Fatal("an archive with too many peers was accepted")
	}
}
