package portability

import (
	"encoding/json/v2"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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

// An archive from before key rotation imports each storage account with the 90 days the migration
// gave every account, and one that already carries the setting is refused as malformed.
func TestOlderArchivesImportAccountsRotatingEvery90Days(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	account := Row{}
	i := slices.IndexFunc(tables, func(t table) bool { return t.Name == "backup_storage_accounts" })
	for column := range strings.SplitSeq(tables[i].Columns, ",") {
		setValue(account, column, "x")
	}
	setValue(account, "id", uuid.New())
	setValue(account, "workspace_id", a.Workspace)
	setValue(account, "peers", []uuid.UUID{})
	setValue(account, "generation", 1)
	setValue(account, "key_rotation_days", 30)
	a.Tables["backup_storage_accounts"] = []Row{account}
	a.Version = 21
	raw, _ = stablejson.Marshal(a)
	if _, err = decodeArchive(raw); err == nil {
		t.Fatal("a version 21 archive carried a key rotation")
	}
	delete(account, "key_rotation_days")
	raw, _ = stablejson.Marshal(a)
	got, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	var days int
	if err = json.Unmarshal(got.Tables["backup_storage_accounts"][0]["key_rotation_days"], &days); err != nil || days != 90 {
		t.Fatalf("key_rotation_days = %d, %v", days, err)
	}
}
