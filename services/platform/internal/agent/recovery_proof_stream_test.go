package agent

import (
	"bytes"
	"encoding/json/jsontext"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// Host evidence keeps this digest across an agent update, and a handover issued
// before the update is refused if the recomputed value moves.
func TestRecoveryProofLinesKeepTheirReleasedDigest(t *testing.T) {
	j, _ := testVisualJournal(t)
	id, parent := uuid.MustParse("11111111-1111-4111-8111-111111111111"), uuid.MustParse("22222222-2222-4222-8222-222222222222")
	receipt := jsontext.Value(`{"phase":"a && b <c>","observation":{"resources":[{"logs":"dropped"}]}}`)
	record := Record{
		Command:    api.ExecutionCommand{Id: id, ParentId: &parent, ServerId: id, Kind: "crosshost", Action: "a && b <c>", Generation: 1, PlanHash: "hash", Deadline: time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)},
		Checkpoint: "after <c>",
		Report:     api.AgentReport{Id: id, Generation: 1, PlanHash: "hash", Sequence: 1, State: "succeeded", Note: "replaced", Crosshost: &receipt},
	}
	if err := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "operations", id.String(), record) }); err != nil {
		t.Fatal(err)
	}
	var lines bytes.Buffer
	digest, err := j.writeRecoveryProofs(&lines)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "68f7134d36ad504c288a8f9d282d1e38a7e85d4e62fd8bf441a1e4874d73335a" {
		t.Fatalf("digest changed to %s for %s", digest, lines.Bytes())
	}
}
