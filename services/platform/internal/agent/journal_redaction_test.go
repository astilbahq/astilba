package agent

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// releaseCanary stands for every value management releases for one operation.
const releaseCanary = "released-value-canary-7f3a"

// releasedCommand is a command of each kind that carries values, as management delivers it, or
// without them, as it delivers one whose values the agent already holds.
func releasedCommand(t *testing.T, plan deployment.Plan, kind api.ExecutionCommandKind, id uuid.UUID, generation int64, values bool) api.ExecutionCommand {
	t.Helper()
	credential := pipeline.Credential{Username: "agent", Password: releaseCanary}
	access := backup.Access{DatabasePassword: releaseCanary, AccessKey: "key-id", SecretKey: releaseCanary, Password: releaseCanary}
	secrets := map[string]string{uuid.NewSHA1(id, []byte("secret")).String(): releaseCanary}
	if !values {
		credential, access, secrets = pipeline.Credential{}, backup.Access{}, nil
	}
	c := api.ExecutionCommand{Id: id, ServerId: plan.ServerID, EnvironmentId: plan.EnvironmentID, ResourceId: plan.UnitID, Kind: kind, Action: "inspect", Generation: generation, Deadline: time.Now().Add(time.Minute)}
	var body any
	switch kind {
	case "visual":
		p := deployment.Payload{Action: "deploy", Plan: plan, Secrets: secrets}
		if values {
			p.RegistryAccess = map[string]pipeline.Credential{"registry.example.test": credential}
		}
		body, c.PlanHash = p, p.Digest()
	case "crosshost":
		parent := uuid.NewSHA1(id, []byte("parent"))
		c.ParentId = &parent
		p := deployment.CrossHostCommand{ParentID: parent, GroupHash: strings.Repeat("a", 64), Phase: "prepare", Unit: deployment.DistributedUnit{Plan: plan}, Secrets: secrets}
		if values {
			p.RegistryAccess = map[string]pipeline.Credential{"registry.example.test": credential}
		}
		body, c.PlanHash = p, p.Digest()
	case "pipeline":
		p := pipeline.Command{Action: "build", Commit: strings.Repeat("b", 40), UnitID: plan.UnitID, BuildSecrets: secrets}
		if values {
			p.Access = &pipeline.RunnerAccess{Clone: credential}
			p.RegistryAccess = &pipeline.RegistryAccess{Push: credential, Pull: credential}
		}
		body, c.PlanHash = p, p.Digest()
	case "backup", "schedule":
		p := backup.Command{Action: "backup", RunID: uuid.NewSHA1(id, []byte("run")), Installation: backup.Installation{Policy: backup.Policy{ID: uuid.NewSHA1(id, []byte("policy")), ServerID: plan.ServerID, EnvironmentID: plan.EnvironmentID}, Access: access}}
		body, c.PlanHash = p, p.Digest()
	case "upgrade":
		p := upgrade.Command{Action: "prepare", Plan: upgrade.Plan{ID: plan.UnitID, Source: plan}, Installation: backup.Installation{Access: access}, Secrets: secrets}
		body, c.PlanHash = p, p.Digest()
	default:
		t.Fatal("no fixture for", kind)
	}
	raw, err := stablejson.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	message := jsontext.Value(raw)
	*commandBody(&c) = &message
	return c
}

var releasedKinds = []api.ExecutionCommandKind{"visual", "crosshost", "pipeline", "backup", "schedule", "upgrade"}

// sealedRecord is how a record reads back from the journal, as bytes.
func sealedRecord(t *testing.T, j *Journal, id uuid.UUID) []byte {
	t.Helper()
	r, err := j.Record(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := stablejson.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// journalPlaintext opens every value in every bucket with the journal's keys.
func journalPlaintext(t *testing.T, j *Journal) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	if err := j.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			return b.ForEach(func(k, v []byte) error {
				if v == nil {
					return nil
				}
				plain, err := j.keys.Open(v, string(name)+":"+string(k))
				if err != nil {
					return err
				}
				out[string(name)+":"+string(k)] = plain
				return nil
			})
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func requireNoCanary(t *testing.T, j *Journal) {
	t.Helper()
	for key, plain := range journalPlaintext(t, j) {
		if bytes.Contains(plain, []byte(releaseCanary)) {
			t.Fatalf("%s still holds a released value", key)
		}
	}
	file, err := os.ReadFile(filepath.Join(j.dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(file, []byte(releaseCanary)) {
		t.Fatal("the journal file holds a released value in plaintext")
	}
}

// writeAsEarlierAgent seals a value the way an agent without redaction did, bypassing put.
func writeAsEarlierAgent(t *testing.T, j *Journal, bucket, key string, value any) {
	t.Helper()
	raw, err := stablejson.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.db.Update(func(tx *bolt.Tx) error {
		data, err := j.keys.Seal(raw, bucket+":"+key)
		if err != nil {
			return err
		}
		return tx.Bucket([]byte(bucket)).Put([]byte(key), data)
	}); err != nil {
		t.Fatal(err)
	}
}

// sealedRows is every value in the open journal, as stored.
func sealedRows(t *testing.T, j *Journal) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	if err := j.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			return b.ForEach(func(k, v []byte) error {
				out[string(name)+":"+string(k)] = bytes.Clone(v)
				return nil
			})
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func reopen(t *testing.T, j *Journal) *Journal {
	t.Helper()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(j.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { next.Close() })
	return next
}

// Every kind of command keeps its values while it runs and until management has its outcome, and
// none once it has. The record still matches its own delivery, with or without values, so
// redelivery and report retries go on working.
func TestFinishedRecordsKeepNoReleasedValues(t *testing.T) {
	j, plan := testVisualJournal(t)
	generation := int64(0)
	for _, kind := range releasedKinds {
		generation++
		id := uuid.New()
		full := releasedCommand(t, plan, kind, id, generation, true)
		valueFree := releasedCommand(t, plan, kind, id, generation, false)
		valueFree.Deadline = full.Deadline
		if full.PlanHash != valueFree.PlanHash || !sameCommand(full, valueFree) {
			t.Fatal(kind, "fixture: a value-free delivery differs from the command")
		}
		r, err := j.Accept(full)
		if err != nil {
			t.Fatal(kind, err)
		}
		if !bytes.Contains(sealedRecord(t, j, id), []byte(releaseCanary)) {
			t.Fatal(kind, "an accepted command lost the values it runs with")
		}
		if err = j.Mutate(id, func(r *Record) { r.Report.State = "succeeded" }); err != nil {
			t.Fatal(err)
		}
		if err = j.Acknowledge(id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(sealedRecord(t, j, id), []byte(releaseCanary)) {
			t.Fatal(kind, "a record lost its values before management acknowledged its outcome")
		}
		if err = j.Acknowledge(id, r.Report.Sequence+1); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(sealedRecord(t, j, id), []byte(releaseCanary)) {
			t.Fatal(kind, "a finished record kept its values")
		}
		stored, err := j.Record(id)
		if err != nil || stored.Command.PlanHash != full.PlanHash || !stored.Acknowledged || stored.Report.State != "succeeded" {
			t.Fatal(kind, "redaction changed the record's identity or outcome", err)
		}
		for _, delivery := range []api.ExecutionCommand{full, valueFree} {
			if !sameCommand(stored.Command, delivery) {
				t.Fatal(kind, "a redacted record no longer matches its command")
			}
			if again, err := j.Accept(delivery); err != nil || again.Report.State != "succeeded" {
				t.Fatal(kind, "a redelivery of a finished command was refused", err)
			}
		}
		if bytes.Contains(sealedRecord(t, j, id), []byte(releaseCanary)) {
			t.Fatal(kind, "a redelivery put values back into a finished record")
		}
	}
	requireNoCanary(t, j)
}

// Records management still needs reported, or that may still run, keep their values, also across
// a restart.
func TestUnfinishedRecordsKeepTheirValues(t *testing.T) {
	j, plan := testVisualJournal(t)
	running := releasedCommand(t, plan, "visual", uuid.New(), 1, true)
	if _, err := j.Accept(running); err != nil {
		t.Fatal(err)
	}
	unreported := releasedCommand(t, plan, "pipeline", uuid.New(), 2, true)
	attention := releasedCommand(t, plan, "crosshost", uuid.New(), 3, true)
	if err := j.db.Update(func(tx *bolt.Tx) error {
		if err := j.put(tx, "operations", unreported.Id.String(), Record{Command: unreported, Report: api.AgentReport{Id: unreported.Id, State: "failed", Sequence: 2}}); err != nil {
			return err
		}
		// Only a terminal outcome finishes a record, whatever marks it acknowledged.
		return j.put(tx, "operations", attention.Id.String(), Record{Command: attention, Report: api.AgentReport{Id: attention.Id, State: "needs_attention", Sequence: 2}, Acknowledged: true})
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for _, c := range []api.ExecutionCommand{running, unreported, attention} {
			if !bytes.Contains(sealedRecord(t, j, c.Id), []byte(releaseCanary)) {
				t.Fatal(c.Kind, "an unfinished record lost its values")
			}
		}
		j = reopen(t, j)
	}
}

// Records an earlier agent finished keep their values on disk until this agent opens the journal,
// including records an earlier agent writes after a rollback. Opening again rewrites nothing.
func TestOpenRedactsRecordsAnEarlierAgentLeft(t *testing.T) {
	j, plan := testVisualJournal(t)
	finished := map[uuid.UUID]api.ExecutionCommandKind{}
	generation := int64(0)
	for _, kind := range releasedKinds {
		generation++
		c := releasedCommand(t, plan, kind, uuid.New(), generation, true)
		writeAsEarlierAgent(t, j, "operations", c.Id.String(), Record{Command: c, Report: api.AgentReport{Id: c.Id, State: "succeeded", Sequence: 2}, Checkpoint: "accepted", Acknowledged: true})
		finished[c.Id] = kind
	}
	pending := releasedCommand(t, plan, "visual", uuid.New(), generation+1, true)
	writeAsEarlierAgent(t, j, "operations", pending.Id.String(), Record{Command: pending, Report: api.AgentReport{Id: pending.Id, State: "succeeded", Sequence: 2}, Checkpoint: "accepted"})
	// A distributed fence shares the bucket and is never treated as a record.
	fence := uuid.New()
	writeAsEarlierAgent(t, j, "distributed-fences", fence.String(), distributedFence{UnitID: uuid.New()})
	writeAsEarlierAgent(t, j, "operations", fence.String(), Record{Command: api.ExecutionCommand{Id: fence, Kind: "visual"}, Report: api.AgentReport{State: "needs_attention"}, Checkpoint: "distributed-runtime-fence"})
	ended, active := upgradeWithValues(t, plan, "complete"), upgradeWithValues(t, plan, "ready")
	writeAsEarlierAgent(t, j, "upgrades", ended.Command.Plan.ID.String(), ended)
	writeAsEarlierAgent(t, j, "upgrades", active.Command.Plan.ID.String(), active)
	fenceBefore := sealedRows(t, j)

	for round := range 2 {
		j = reopen(t, j)
		for id, kind := range finished {
			stored := sealedRecord(t, j, id)
			if bytes.Contains(stored, []byte(releaseCanary)) {
				t.Fatal(kind, "opening kept a finished record's values")
			}
		}
		if !bytes.Contains(sealedRecord(t, j, pending.Id), []byte(releaseCanary)) {
			t.Fatal("opening redacted a record management has not acknowledged")
		}
		stored, err := j.upgrade(ended.Command.Plan.ID)
		if err != nil || len(stored.Command.Secrets) != 0 || stored.Command.Installation.Access != (backup.Access{}) {
			t.Fatal("opening kept an ended upgrade's values", err)
		}
		stored, err = j.upgrade(active.Command.Plan.ID)
		if err != nil || len(stored.Command.Secrets) == 0 || stored.Command.Installation.Access == (backup.Access{}) {
			t.Fatal("opening removed the values an earlier agent needs for an active upgrade", err)
		}
		if round == 0 {
			// Rolled back, an earlier agent finishes another command without redacting it.
			c := releasedCommand(t, plan, "pipeline", uuid.New(), generation+2, true)
			writeAsEarlierAgent(t, j, "operations", c.Id.String(), Record{Command: c, Report: api.AgentReport{Id: c.Id, State: "failed", Sequence: 2}, Checkpoint: "accepted", Acknowledged: true})
			finished[c.Id] = c.Kind
		}
	}
	after := sealedRows(t, j)
	for _, key := range []string{"operations:" + fence.String(), "distributed-fences:" + fence.String()} {
		if !bytes.Equal(fenceBefore[key], after[key]) {
			t.Fatal("opening rewrote a fence", key)
		}
	}
	before := sealedRows(t, j)
	j = reopen(t, j)
	if !unchangedRows(before, sealedRows(t, j)) {
		t.Fatal("opening a redacted journal rewrote records")
	}
}

// The open's redaction is one transaction: when any record can't be read, the journal refuses to
// open and no record is rewritten, so none is left half redacted.
func TestOpenRedactionIsAllOrNothing(t *testing.T) {
	j, plan := testVisualJournal(t)
	first := releasedCommand(t, plan, "visual", uuid.MustParse("00000000-0000-4000-8000-000000000001"), 1, true)
	writeAsEarlierAgent(t, j, "operations", first.Id.String(), Record{Command: first, Report: api.AgentReport{Id: first.Id, State: "succeeded", Sequence: 2}, Acknowledged: true})
	if err := j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("operations")).Put([]byte("ffffffff-ffff-4fff-bfff-ffffffffffff"), []byte("unreadable"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	before := encryptedJournalRows(t, j.dir)
	if reopened, err := Open(j.dir); err == nil {
		reopened.Close()
		t.Fatal("a journal with an unreadable record opened")
	}
	if !unchangedRows(before, encryptedJournalRows(t, j.dir)) {
		t.Fatal("a failed open rewrote records")
	}
}

func upgradeWithValues(t *testing.T, plan deployment.Plan, phase string) upgradeState {
	t.Helper()
	id := uuid.New()
	c := releasedCommand(t, plan, "upgrade", id, 1, true)
	var body upgrade.Command
	if err := json.Unmarshal(*c.Upgrade, &body); err != nil {
		t.Fatal(err)
	}
	body.Plan.ID = id
	return upgradeState{OperationID: id, Command: body, Status: upgrade.Status{Phase: phase}}
}

// An upgrade keeps its values while an action may still be run on it, for an earlier agent
// restored onto this journal, and none once it has ended. No action is allowed on an upgrade
// without them, so an earlier agent never reads the values it lost.
func TestEndedUpgradesKeepNoValues(t *testing.T) {
	j, plan := testVisualJournal(t)
	for _, phase := range []string{"planned", "running", "ready", "attention", "complete", "returned", "rejected"} {
		s := upgradeWithValues(t, plan, phase)
		if err := j.saveUpgrade(s); err != nil {
			t.Fatal(err)
		}
		stored, err := j.upgrade(s.Command.Plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		kept := len(stored.Command.Secrets) > 0 && stored.Command.Installation.Access != (backup.Access{})
		if kept != !slices.Contains([]string{"complete", "returned", "rejected"}, phase) {
			t.Fatal(phase, "upgrade values kept:", kept)
		}
		if stored.Command.Digest() != s.Command.Digest() {
			t.Fatal(phase, "removing values changed the upgrade's digest")
		}
		if !kept && slices.ContainsFunc(upgradeActions, stored.Status.Allows) {
			t.Fatal(phase, "an upgrade without values still allows an action")
		}
	}
}

// Redaction edits members, so a member this agent doesn't know, which a later agent's digest may
// cover, survives. A payload it can't read is dropped rather than kept with its values, and an
// unset credential is not a value.
func TestRedactionKeepsUnknownMembersAndDropsUnreadablePayloads(t *testing.T) {
	plan := deployment.Plan{ID: uuid.New(), ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: uuid.New()}
	c := releasedCommand(t, plan, "visual", uuid.New(), 1, true)
	raw := strings.Replace(string(*c.Visual), `{`, `{"laterMember":{"kept":true},`, 1)
	message := jsontext.Value(raw)
	c.Visual = &message
	if !redactCommand(&c) || c.Visual == nil {
		t.Fatal("a payload with values was not redacted")
	}
	if strings.Contains(string(*c.Visual), releaseCanary) || !strings.Contains(string(*c.Visual), `"laterMember":{"kept":true}`) {
		t.Fatal("redaction lost an unknown member or kept a value", string(*c.Visual))
	}
	if redactCommand(&c) {
		t.Fatal("redacting twice changed the payload again")
	}

	unreadable := jsontext.Value(`"` + releaseCanary + `"`)
	c.Visual = &unreadable
	if !redactCommand(&c) || c.Visual != nil {
		t.Fatal("an unreadable payload was kept")
	}

	unset := releasedCommand(t, plan, "backup", uuid.New(), 1, false)
	before := string(*unset.Backup)
	if !strings.Contains(before, `"access":{`) || redactCommand(&unset) || string(*unset.Backup) != before {
		t.Fatal("a backup command without credentials was rewritten")
	}

	for _, kind := range []api.ExecutionCommandKind{"network", "update", "test"} {
		if _, ok := releasedValues[kind]; ok {
			t.Fatal(kind, "carries no released values")
		}
	}
}
