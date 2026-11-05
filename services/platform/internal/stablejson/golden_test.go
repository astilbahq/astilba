package stablejson_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
)

var (
	fixtureID   = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	fixtureTime = time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)
	fixtureText = "sh -c 'a && b <c> >d' " + string(rune(0x2028))
)

func fixturePlan() deployment.Plan {
	return deployment.Plan{ID: fixtureID, ServerID: fixtureID, EnvironmentID: fixtureID, UnitID: fixtureID, RevisionID: fixtureID, ExpectedState: fixtureText, ExpiresAt: fixtureTime, Specification: domain.Specification{Version: 1}, Images: map[string]string{"z": "1", "a": fixtureText, "m": "3"}}
}

// These digests were recorded from encoding/json v1. A controller and an agent on
// different releases, a journal and a recovery bundle all compare against them.
//
// Each value keeps nil slices and maps, zero omitempty fields and characters that
// v1 and v2 write differently. If this fails after adding a struct field, make the
// field omit its zero value: older peers will not know to expect it.
func TestDigestsKeepTheirReleasedValues(t *testing.T) {
	empty := ""
	for name, c := range map[string]struct{ got, want string }{
		"deployment plan":    {fixturePlan().WithHash().Hash, "4b62f2f92bcd67ec669fcb40a65540e37bccbc20733035605c85cf7c794d686c"},
		"deployment payload": {deployment.Payload{Action: fixtureText, Plan: fixturePlan()}.Digest(), "79de51650e74cc3277d7f7c9a5339b74ce7664d99f6452d64a5497bae0360db2"},
		"crosshost command":  {deployment.CrossHostCommand{ParentID: fixtureID, Phase: fixtureText, Unit: deployment.DistributedUnit{Plan: fixturePlan()}}.Digest(), "321f9c8a89bbd1ae2b75bc6d809b48a22a1ee24cd3a12e6337d66e21d0b3d964"},
		// The agent checks this before replacing itself, so it is the way out of any
		// version skew. If it moves, older agents can no longer be updated.
		"agent update":     {execution.Hash(&api.AgentRelease{Version: "1.2.3", Protocol: 1, Arch: "amd64", Size: 10, Sha256: "abc", Signature: "c2ln"}), "79aeb2511ac7f04786d42ee6c6728efcca3f99ec16ab6f06886d627cc19b934c"},
		"railpack policy":  {pipeline.Hash(json.RawMessage(pipeline.RailpackSourcePolicy(pipeline.RailpackInputs{}))), "dfda240d8e63dbca7e0346203c386b54a7e9498f226f20b8d1cae26695af15fd"},
		"pipeline command": {pipeline.Command{Action: fixtureText, SubmissionID: fixtureID}.Digest(), "6761729faa7743cf39ec678c7924953fe79f09db951b0439dfa45eb49c722f48"},
		"backup command":   {backup.Command{Action: fixtureText, RunID: fixtureID, ScheduledAt: &fixtureTime}.Digest(), "b46c63ebce0d6ba5b5cb1c183e466015bea68dce2eab6c25b99a26bf733acb9b"},
		"upgrade command":  {upgrade.Command{Action: fixtureText, Plan: upgrade.Plan{ID: fixtureID}}.Digest(), "fad33cca183644bcfd9e57f2917147dad328f0d842b60c6b75319be60e5744af"},
		// Cloned through JSON. Recovery recomputes this and compares it with a stored plan.
		"upgrade target":   {upgrade.Plan{TargetPlanID: fixtureID, TargetRevisionID: fixtureID, Source: fixturePlan()}.Target().Hash, "5fc25a5c23138cafa15ec23cafb5e594ff82d9b5746d37fb7e203fadcd7f539b"},
		"network manifest": {hostnetwork.Hash(hostnetwork.Manifest{NetworkID: fixtureID, PublicKey: fixtureText}), "4067adfad76af5bf7f6bc0e833a80c69a679e946d4ecad5b8f2f12cac3f4bfd5"},
		"execution hash": {execution.Hash(struct {
			Text  string
			Empty *string `json:"empty,omitempty"`
			Off   bool    `json:"off,omitempty"`
			None  []string
		}{fixtureText, &empty, false, nil}), "ebe6f76b01a6cfb11fed997e7296524d9b6465eb1a9f06dea99c4d7f4a6a9ca9"},
		"raw message and bytes": {pipeline.Hash(map[string]any{"z": json.RawMessage(`{ "a" : "<" }`), "a": []byte(fixtureText)}), "ad97cfd776d61e9f3dffd9ca020784c1408bb4c09d729517b6b1647502bbd170"},
		// Docker health checks hold time.Duration, which plain v2 refuses to encode.
		// Agent fingerprints ignore Marshal errors, so they would hash only the output
		// before the failing field and miss drift in anything after it.
		"duration":      {deployment.Hash(struct{ Interval time.Duration }{90 * time.Second}), "6f9617e97fc6ebc51e9402dfa971345beacfd7e67f71dad6fb094ecda6cd7f52"},
		"invalid UTF-8": {deployment.Hash("a\xffb"), "da0b6b76a1813c97f0710c9b5e238b54c57ea04dfa0f794627e1ce8f500a6975"},
	} {
		if c.got != c.want {
			t.Errorf("%s digest changed to %s", name, c.got)
		}
	}
}

func TestStoredSpecificationKeepsItsContentHash(t *testing.T) {
	_, canonical, err := domain.ParseSpec([]byte(`{"version":1,"applications":[],"variables":[{"name":"A","value":"x && <y>"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if h := sha256.Sum256(canonical); hex.EncodeToString(h[:]) != "0b32b999ec1d6a24d533e0b7b8b0b2a74a72f02053a1e0a6d93a3dd5f59d7353" {
		t.Fatalf("content hash changed for %s", canonical)
	}
}

// A command crosses the wire as JSON and its reader recomputes the digest, so the
// encoding must survive a round trip through both decoders.
func TestDigestsSurviveTheWireAcrossVersions(t *testing.T) {
	want := deployment.Payload{Action: fixtureText, Plan: fixturePlan()}
	raw, err := stablejson.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var older, newer deployment.Payload
	if err = json.Unmarshal(raw, &older); err != nil {
		t.Fatal(err)
	}
	if err = jsonv2.Unmarshal(raw, &newer, jsonv2.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	if older.Digest() != want.Digest() || newer.Digest() != want.Digest() {
		t.Fatalf("digest moved: older %s, newer %s, want %s", older.Digest(), newer.Digest(), want.Digest())
	}
}
