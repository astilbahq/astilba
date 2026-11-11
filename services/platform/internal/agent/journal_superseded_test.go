package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	bolt "go.etcd.io/bbolt"
)

// finished accepts a command and records it as run, reported and acknowledged.
func finished(t *testing.T, j *Journal, c api.ExecutionCommand) Record {
	t.Helper()
	r, err := j.Accept(c)
	if err != nil {
		t.Fatal(c.Generation, err)
	}
	if err = j.Mutate(r.Command.Id, func(r *Record) { r.Report.State = "succeeded" }); err != nil {
		t.Fatal(err)
	}
	if err = j.Acknowledge(r.Command.Id, r.Report.Sequence+1); err != nil {
		t.Fatal(err)
	}
	return r
}

// A command below the counter either never ran here or ran and was forgotten. Only the first may
// be called unrun: the second's outcome is unknown, so it stays refused.
func TestSupersededOnlyWhenTheJournalKnowsItNeverRan(t *testing.T) {
	j, plan := testVisualJournal(t)
	accepted := map[int64]Record{}
	for generation := int64(1); generation <= 129; generation++ {
		accepted[generation] = finished(t, j, visualCommand(plan, "deploy", generation))
	}
	var forgotten Record
	for _, r := range accepted {
		if _, err := j.Record(r.Command.Id); errors.Is(err, os.ErrNotExist) {
			forgotten = r
		}
	}
	if forgotten.Command.Generation == 0 {
		t.Fatal("the journal forgot nothing at its limit")
	}
	if _, err := j.Accept(forgotten.Command); err == nil {
		t.Fatal("a forgotten command that ran was accepted again")
	}
	if _, err := j.Record(forgotten.Command.Id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a forgotten command that ran was recorded as unrun", err)
	}
	unrun := visualCommand(plan, "deploy", 129)
	if r, err := j.Accept(unrun); err != nil || r.Report.State != "failed" || r.Checkpoint != "superseded" {
		t.Fatal("a command above everything forgotten was not ended unrun", r.Report.State, err)
	}
}

// A new controller's counters start again, below the generations of the records the journal kept
// from the old one. Forgetting those must not refuse the new controller's unrun commands.
func TestForgottenRecordsOfAnEarlierControllerDontCount(t *testing.T) {
	j, plan := testVisualJournal(t)
	for generation := int64(1000); generation < 1128; generation++ {
		finished(t, j, visualCommand(plan, "deploy", generation))
	}
	if err := j.Transfer("https://transferred.example.test", make([]byte, 32), 1, strings.Repeat("t", 43)); err != nil {
		t.Fatal(err)
	}
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enrolled = true
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	controller := int64(1)
	command := func(generation int64) api.ExecutionCommand {
		c := visualCommand(plan, "deploy", generation)
		c.ControllerGeneration = &controller
		return c
	}
	// The journal is full of the earlier controller's records, so this forgets one of them.
	finished(t, j, command(2))
	if r, err := j.Accept(command(1)); err != nil || r.Report.State != "failed" || r.Checkpoint != "superseded" {
		t.Fatal("forgetting the earlier controller's records refused an unrun command", r.Report.State, err)
	}
}

// A clean close vouches that nothing else wrote, so an overtaken command still ends unrun after it.
func TestCleanRestartKeepsWhatTheJournalKnows(t *testing.T) {
	j, plan := testVisualJournal(t)
	finished(t, j, visualCommand(plan, "deploy", 5))
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(j.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Close() })
	if r, err := restarted.Accept(visualCommand(plan, "deploy", 3)); err != nil || r.Report.State != "failed" || r.Checkpoint != "superseded" {
		t.Fatal("a clean restart forgot that the command never ran", r.Report.State, err)
	}
}

// An earlier agent forgets commands without recording it, and a crash can cut this one short, so
// after either, any command up to the counters may have run. Cancelling one still ends it.
func TestAnotherWriterLeavesThePastUnknown(t *testing.T) {
	for name, stop := range map[string]func(*testing.T, *Journal){
		"an earlier agent wrote": func(t *testing.T, j *Journal) {
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(filepath.Join(j.dir, "journal.db"), 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			// What an earlier agent does as it opens the journal.
			if err = db.Update(func(tx *bolt.Tx) error { _, e := tx.CreateBucketIfNotExists([]byte("operations")); return e }); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
		},
		"the agent stopped uncleanly": func(t *testing.T, j *Journal) {
			if err := j.db.Close(); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			j, plan := testVisualJournal(t)
			finished(t, j, visualCommand(plan, "deploy", 5))
			stop(t, j)
			reopened, err := Open(j.dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reopened.Close() })
			overtaken := visualCommand(plan, "deploy", 3)
			if _, err = reopened.Accept(overtaken); err == nil {
				t.Fatal("a command that may have run was called unrun")
			}
			overtaken.CancelRequested = true
			r, err := reopened.Accept(overtaken)
			if err != nil || r.Report.State != "cancelled" || !strings.Contains(r.Report.Note, "may have run") {
				t.Fatal("cancelling a command of unknown outcome didn't end it honestly", r.Report, err)
			}
			if err = reopened.Acknowledge(r.Command.Id, r.Report.Sequence); err != nil {
				t.Fatal(err)
			}
			if r, err = reopened.Accept(visualCommand(plan, "deploy", 6)); err != nil || r.Report.State != "running" {
				t.Fatal("the journal refused a new command", err)
			}
		})
	}
}
