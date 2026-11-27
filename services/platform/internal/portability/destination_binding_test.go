package portability

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"os"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// bindingArchive is the fixture workspace with two servers, and destinations and policies made
// by add.
type bindingArchive struct {
	Archive
	first, other uuid.UUID
}

func newBindingArchive(t *testing.T) *bindingArchive {
	t.Helper()
	raw, err := os.ReadFile("testdata/workspace-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := decodeArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.Tables = maps.Clone(a.Tables)
	server := a.Tables["servers"][0]
	other := maps.Clone(server)
	id := uuid.New()
	setValue(other, "id", id)
	a.Tables["servers"] = []Row{server, other}
	a.Tables["backup_destinations"], a.Tables["backup_policies"] = []Row{}, []Row{}
	var first uuid.UUID
	if err = jsonValue(server["id"], &first); err != nil {
		t.Fatal(err)
	}
	return &bindingArchive{Archive: a, first: first, other: id}
}

func jsonValue(raw jsontext.Value, out any) error { return json.Unmarshal(raw, out) }

// destination adds a destination of kind, bound as given, and returns its ID.
func (a *bindingArchive) destination(kind string, server *uuid.UUID, shared bool) uuid.UUID {
	id := uuid.New()
	configuration, _ := stablejson.Marshal(map[string]string{"id": id.String(), "kind": kind})
	row := Row{"workspace_id": a.Tables["servers"][0]["workspace_id"], "configuration": configuration, "encrypted_access": jsontext.Value(`null`)}
	setValue(row, "id", id)
	setValue(row, "server_id", server)
	setValue(row, "shared", shared)
	a.Tables["backup_destinations"] = append(a.Tables["backup_destinations"], row)
	return id
}

// policy adds a policy on server using destination.
func (a *bindingArchive) policy(server, destination uuid.UUID, method string) {
	configuration, _ := stablejson.Marshal(map[string]string{"method": method, "serverId": server.String(), "destinationId": destination.String()})
	row := Row{"workspace_id": a.Tables["servers"][0]["workspace_id"], "environment_id": a.Tables["environments"][0]["id"], "configuration": configuration, "encrypted_access": jsontext.Value(`null`), "status": jsontext.Value(`{}`), "status_sequence": jsontext.Value(`0`), "snapshot_receipts": jsontext.Value(`{}`)}
	setValue(row, "id", uuid.New())
	setValue(row, "server_id", server)
	a.Tables["backup_policies"] = append(a.Tables["backup_policies"], row)
}

func (a *bindingArchive) decode(t *testing.T, version int) (Archive, error) {
	t.Helper()
	out := a.Archive
	out.Version = version
	out.Tables = maps.Clone(a.Tables)
	if version < 19 {
		withoutVersionNineteen(out)
	}
	raw, err := stablejson.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return decodeArchive(raw)
}

// An archive from before destinations were bound has them bound as migration 00075 binds a
// database's: to the one server their policies are on, shared when those are on several, and
// unbound when no policy, or only a container job, names them. Filesystem destinations stay unbound.
func TestOlderArchivesBindTheirDestinations(t *testing.T) {
	a := newBindingArchive(t)
	oneServer, twoServers, unused, jobOnly, local := a.destination("s3", nil, false), a.destination("s3", nil, false), a.destination("s3", nil, false), a.destination("s3", nil, false), a.destination("filesystem", nil, false)
	a.policy(a.first, oneServer, "postgres-logical")
	a.policy(a.first, oneServer, "quiesced-files")
	a.policy(a.first, twoServers, "postgres-logical")
	a.policy(a.other, twoServers, "quiesced-files")
	a.policy(a.other, jobOnly, "container-job")
	a.policy(a.first, local, "quiesced-files")
	a.policy(a.other, local, "quiesced-files")
	got, err := a.decode(t, 17)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uuid.UUID]string{oneServer: `"` + a.first.String() + `"/false`, twoServers: "null/true", unused: "null/false", jobOnly: "null/false", local: "null/false"}
	for _, row := range got.Tables["backup_destinations"] {
		var id uuid.UUID
		if err = jsonValue(row["id"], &id); err != nil {
			t.Fatal(err)
		}
		if binding := string(row["server_id"]) + "/" + string(row["shared"]); binding != want[id] {
			t.Errorf("destination %s bound %s, want %s", id, binding, want[id])
		}
	}
	// A version 17 archive that already names a server wasn't written by an export.
	b := newBindingArchive(t)
	b.destination("s3", &b.first, false)
	out := b.Archive
	out.Version = 17
	raw, _ := stablejson.Marshal(out)
	if _, err = decodeArchive(raw); err == nil {
		t.Error("a version 17 archive carried a destination's server")
	}
}

// An archive's bindings must be ones a database holds, and its policies must keep to them.
func TestArchiveBindingsMatchTheirPolicies(t *testing.T) {
	cases := map[string]func(a *bindingArchive){
		"a bound filesystem destination":     func(a *bindingArchive) { a.destination("filesystem", &a.first, false) },
		"a shared filesystem destination":    func(a *bindingArchive) { a.destination("filesystem", nil, true) },
		"a shared destination with a server": func(a *bindingArchive) { a.destination("s3", &a.first, true) },
		"a policy on another server's destination": func(a *bindingArchive) {
			a.policy(a.other, a.destination("s3", &a.first, false), "postgres-logical")
		},
		"a policy on an unbound destination": func(a *bindingArchive) {
			a.policy(a.first, a.destination("s3", nil, false), "postgres-logical")
		},
		// A restore reads the server its configuration names, and delivering keys its row's.
		"a policy whose configuration names another server": func(a *bindingArchive) {
			destination := a.destination("s3", &a.other, false)
			a.policy(a.other, destination, "postgres-logical")
			row := a.Tables["backup_policies"][len(a.Tables["backup_policies"])-1]
			row["configuration"], _ = stablejson.Marshal(map[string]string{"method": "postgres-logical", "serverId": a.first.String(), "destinationId": destination.String()})
		},
	}
	for name, change := range cases {
		a := newBindingArchive(t)
		change(a)
		if _, err := a.decode(t, ArchiveVersion); err == nil {
			t.Errorf("an archive with %s was accepted", name)
		}
	}
	a := newBindingArchive(t)
	bound := a.destination("s3", &a.first, false)
	a.policy(a.first, bound, "postgres-logical")
	shared := a.destination("s3", nil, true)
	a.policy(a.first, shared, "postgres-logical")
	a.policy(a.other, shared, "postgres-logical")
	a.policy(a.other, a.destination("s3", nil, false), "container-job")
	a.policy(a.other, a.destination("filesystem", nil, false), "quiesced-files")
	if _, err := a.decode(t, ArchiveVersion); err != nil {
		t.Fatal("an archive whose policies keep to their bindings was refused:", err)
	}
}
