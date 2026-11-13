package agent

import (
	"encoding/json/jsontext"
	"os"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// An earlier agent kept each submission's repository write credential in its journal. Opening the
// journal drops it, and leaves every other command, and its clone credential, as it was. A
// submission accepted later is stored without one.
func TestJournalDropsSubmissionWriteCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	command := func(pipeline string) api.ExecutionCommand {
		raw := jsontext.Value(pipeline)
		return api.ExecutionCommand{Id: uuid.New(), Kind: "pipeline", Pipeline: &raw}
	}
	submit := command(`{"action":"submit","commit":"` + strings.Repeat("a", 40) + `","access":{"api":{"username":"x-access-token","password":"write-canary"},"clone":{"username":"","password":""},"webhook":""}}`)
	build := command(`{"action":"build","access":{"clone":{"username":"reader","password":"clone-canary"}}}`)
	// As an earlier agent left them: recorded, and never dropped.
	if err = j.db.Update(func(tx *bolt.Tx) error {
		for _, c := range []api.ExecutionCommand{submit, build} {
			if err := j.put(tx, "operations", c.Id.String(), Record{Command: c}); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte("identity")).Delete([]byte("submission-credentials-dropped"))
	}); err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	if j, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	kept, err := j.Record(submit.Id)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(*kept.Command.Pipeline); strings.Contains(text, "write-canary") || strings.Contains(text, `"access"`) || !strings.Contains(text, strings.Repeat("a", 40)) {
		t.Fatal("the submission kept its write credential, or lost the rest of its command", text)
	}
	other, err := j.Record(build.Id)
	if err != nil || !strings.Contains(string(*other.Command.Pipeline), "clone-canary") {
		t.Fatal("a build lost its clone credential", err)
	}
	// An older management, as after a rollback, can still send one.
	fresh := t.TempDir()
	if err = os.Chmod(fresh, 0700); err != nil {
		t.Fatal(err)
	}
	accepting, err := Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	defer accepting.Close()
	late := command(`{"action":"submit","access":{"api":{"username":"x-access-token","password":"late-write-canary"}}}`)
	late.Generation = 1
	if _, err = accepting.Accept(late); err != nil {
		t.Fatal(err)
	}
	if kept, err = accepting.Record(late.Id); err != nil || strings.Contains(string(*kept.Command.Pipeline), "late-write-canary") {
		t.Fatal("an accepted submission kept its write credential", err)
	}
}
