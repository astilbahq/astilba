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

// The key sweep reads every workspace's key installs and reported policy generations in one
// statement, so an archive is refused unless its installs are the list activation writes and each
// policy's reported generation is a counter.
func TestImportsRefuseInstallsAndGenerationsTheSweepCantRead(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	row := func(name string) Row {
		r := Row{}
		i := slices.IndexFunc(tables, func(t table) bool { return t.Name == name })
		for column := range strings.SplitSeq(tables[i].Columns, ",") {
			setValue(r, column, nil)
		}
		setValue(r, "id", uuid.New())
		setValue(r, "workspace_id", a.Workspace)
		return r
	}
	key, policy := row("backup_storage_keys"), row("backup_policies")
	setValue(policy, "snapshot_receipts", map[string]any{})
	a.Tables["backup_storage_keys"], a.Tables["backup_policies"] = []Row{key}, []Row{policy}
	a.Version = ArchiveVersion
	decodes := func() bool {
		raw, _ := stablejson.Marshal(a)
		_, err := decodeArchive(raw)
		return err == nil
	}
	installs := []any{map[string]any{"policy": uuid.New(), "generation": 3}}
	setValue(key, "installs", installs)
	setValue(policy, "status", map[string]any{"generation": 2, "enabled": true})
	if !decodes() {
		t.Fatal("a well-formed key and policy were refused")
	}
	for _, bad := range []any{
		nil,
		map[string]any{"policy": uuid.New(), "generation": 3},
		[]any{map[string]any{"policy": "x", "generation": 3}},
		// The parser reads both of these, but PostgreSQL refuses to cast them.
		[]any{map[string]any{"policy": "urn:uuid:" + uuid.NewString(), "generation": 3}},
		[]any{map[string]any{"policy": "x" + uuid.NewString() + "y", "generation": 3}},
		[]any{map[string]any{"policy": strings.ToUpper(uuid.NewString()), "generation": 3}},
		[]any{map[string]any{"policy": uuid.New(), "generation": 1.5}},
		[]any{map[string]any{"policy": uuid.New(), "generation": "3"}},
		[]any{map[string]any{"policy": uuid.New()}},
		[]any{map[string]any{"policy": uuid.New(), "generation": 3, "key": "x"}},
	} {
		setValue(key, "installs", bad)
		if decodes() {
			t.Errorf("installs %v were accepted", bad)
		}
	}
	setValue(key, "installs", installs)
	for _, bad := range []any{"x", 1.5, MaxCounter + 1} {
		setValue(policy, "status", map[string]any{"generation": bad})
		if decodes() {
			t.Errorf("a reported generation of %v was accepted", bad)
		}
	}
	// An agent can report a negative generation, so an export can hold one.
	setValue(policy, "status", map[string]any{"generation": -1})
	if !decodes() {
		t.Error("a negative reported generation was refused")
	}
}
