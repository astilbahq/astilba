package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

// A release the agent refuses leaves the host as it was, so the update fails with the reason
// instead of waiting for attention behind a hint at the local procedure, which would refuse it
// too. Any other failure still needs attention.
func TestRefusedUpdateFailsWithItsReason(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := fixtureAgentWithFloor("0.2.0", "0.1.0", false)
	sum := sha256.Sum256(body)
	release := api.AgentRelease{Version: "0.2.0", MinimumVersion: "0.1.0", Commit: fixtureCommit, Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(body)), Sha256: hex.EncodeToString(sum[:])}
	release.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, agentrelease.Message(release)))
	for _, test := range []struct {
		name, state, note string
		update            func(path string) func(context.Context, api.AgentRelease) error
	}{
		{"below the floor", "failed", "The agent refused release 0.2.0: its minimum version 0.1.0 is below 0.3.0, the minimum version of the release the agent runs. Nothing on the host changed. Choose a release whose minimum version is 0.3.0 or later.",
			func(path string) func(context.Context, api.AgentRelease) error {
				return func(ctx context.Context, r api.AgentRelease) error {
					return Install(ctx, r, pub, bytes.NewReader(body), path, Running{MinimumVersion: "0.3.0"})
				}
			}},
		{"for another architecture", "failed", "The agent refused release 0.2.0: wrong host architecture. Nothing on the host changed.",
			func(string) func(context.Context, api.AgentRelease) error {
				return func(context.Context, api.AgentRelease) error {
					return &Refused{Release: "0.2.0", Err: errors.New("wrong host architecture")}
				}
			}},
		{"interrupted", "needs_attention", "Agent update did not finish. Inspect the local update state and retry the local procedure.",
			func(string) func(context.Context, api.AgentRelease) error {
				return func(context.Context, api.AgentRelease) error { return errors.New("artifact unavailable") }
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			j, p := testVisualJournal(t)
			path := filepath.Join(t.TempDir(), "agent")
			current := []byte("current executable")
			if err := os.WriteFile(path, current, 0755); err != nil {
				t.Fatal(err)
			}
			c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, Generation: 1, Kind: "update", Release: &release, PlanHash: execution.Hash(&release), Deadline: time.Now().Add(time.Hour)}
			e := Engine{Journal: j, Update: test.update(path), VerifyInstalledUpdate: func(r api.AgentRelease) error { return VerifyInstalledUpdate(r, pub, path) }}
			if err := e.Execute(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			r, err := j.Record(c.Id)
			if err != nil || r.Report.State != api.ExecutionState(test.state) || r.Report.Note != test.note {
				t.Fatalf("report %q %q, want %q %q (%v)", r.Report.State, r.Report.Note, test.state, test.note, err)
			}
			if got, _ := os.ReadFile(path); !bytes.Equal(got, current) {
				t.Fatal("the refused release replaced the agent")
			}
		})
	}
}
