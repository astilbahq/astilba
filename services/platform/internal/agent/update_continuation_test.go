package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func signedUpdate(t *testing.T, arch string) (api.AgentRelease, ed25519.PublicKey, []byte) {
	t.Helper()
	body := []byte("#!/bin/sh\nprintf '%s\\n' '{\"Version\":\"fixture.2\",\"Protocol\":1,\"Arch\":\"" + runtime.GOARCH + "\"}'\n")
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	release := api.AgentRelease{Version: "fixture.2", Protocol: 1, Arch: api.AgentReleaseArch(arch), Size: int64(len(body)), Sha256: hex.EncodeToString(digest[:])}
	release.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, agentrelease.Message(release)))
	return release, pub, body
}

func TestLegacySignedUpdateRestartPreservesEvidenceAndReconcilesInstalledArtifact(t *testing.T) {
	for _, state := range []api.ExecutionState{"running", "succeeded"} {
		t.Run(string(state), func(t *testing.T) {
			release, pub, body := signedUpdate(t, runtime.GOARCH)
			executable := filepath.Join(t.TempDir(), "agent")
			if err := os.WriteFile(executable, []byte("previous executable"), 0755); err != nil {
				t.Fatal(err)
			}
			var original Record
			dir, _ := legacyJournal(t, func(j *Journal) {
				cfg, _ := j.Config()
				cfg.PublicKey = pub
				if err := j.SetConfig(cfg); err != nil {
					t.Fatal(err)
				}
				c := api.ExecutionCommand{Id: uuid.New(), ServerId: cfg.ServerID, Generation: 1, Kind: "update", Release: &release, PlanHash: execution.Hash(&release), Deadline: time.Now().Add(-time.Hour)}
				// Predecessors have no AcceptedAt metadata. The installing state
				// can survive replacement before a successful report is recorded.
				original = Record{Command: c, Report: api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: state}, Checkpoint: "accepted"}
				if err := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "operations", c.Id.String(), original) }); err != nil {
					t.Fatal(err)
				}
				if err := Install(context.Background(), release, pub, bytes.NewReader(body), executable); err != nil {
					t.Fatal(err)
				}
			})
			j, err := Open(dir)
			if err != nil {
				t.Fatal("pending signed update prevented migration", err)
			}
			defer j.Close()
			r, err := j.Record(original.Command.Id)
			if err != nil || deployment.Hash(r) != deployment.Hash(original) {
				t.Fatal("migration manufactured or changed update progress", err)
			}
			e := Engine{Journal: j, VerifyInstalledUpdate: func(r api.AgentRelease) error { return VerifyInstalledUpdate(r, pub, executable) }, Update: func(context.Context, api.AgentRelease) error {
				t.Fatal("restart downloaded or installed an expired update")
				return nil
			}}
			if err = e.Execute(context.Background(), original.Command); err != nil {
				t.Fatal(err)
			}
			r, err = j.Record(original.Command.Id)
			if err != nil || r.Report.State != "succeeded" || r.Acknowledged || !sameCommand(r.Command, original.Command) {
				t.Fatal("installed update did not reconcile the same unacknowledged command", err)
			}
			if state == "succeeded" && deployment.Hash(r) != deployment.Hash(original) {
				t.Fatal("success-awaiting-acknowledgment was reset on restart")
			}
		})
	}
}

func TestExpiredUpdateFirstAcceptanceCannotAcquireContinuationAcrossRestart(t *testing.T) {
	j, p := testVisualJournal(t)
	release, pub, body := signedUpdate(t, runtime.GOARCH)
	path := filepath.Join(t.TempDir(), "installed")
	if err := os.WriteFile(path, body, 0755); err != nil {
		t.Fatal(err)
	}
	cfg, _ := j.Config()
	cfg.PublicKey = pub
	if err := j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, Generation: 1, Kind: "update", Release: &release, PlanHash: execution.Hash(&release), Deadline: time.Now().Add(-time.Hour)}
	first, err := j.Accept(c)
	if err != nil || first.AcceptedAt == nil || !first.AcceptedAt.After(c.Deadline) {
		t.Fatal("first acceptance lost local expiry provenance", err)
	}
	if _, err = j.Accept(c); err != nil {
		t.Fatal(err)
	}
	repeated, _ := j.Record(c.Id)
	if !first.AcceptedAt.Equal(*repeated.AcceptedAt) {
		t.Fatal("redelivery changed first-acceptance provenance")
	}
	// Fresh expiry keeps its original early cancellation outcome even when
	// unrelated host admission would later reject a database upgrade.
	if err := j.saveUpgrade(upgradeState{Command: upgrade.Command{Plan: upgrade.Plan{ID: uuid.New()}}, Status: upgrade.Status{Phase: "ready"}}); err != nil {
		t.Fatal(err)
	}
	dir := j.dir
	j.Close() // Crash after transport acceptance, before Engine execution.
	j, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	e := Engine{Journal: j, VerifyInstalledUpdate: func(r api.AgentRelease) error { return VerifyInstalledUpdate(r, pub, path) }, Update: func(context.Context, api.AgentRelease) error {
		t.Fatal("expired first delivery performed an update")
		return nil
	}}
	if err = e.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	r, _ := j.Record(c.Id)
	if r.Report.State != "cancelled" {
		t.Fatal("expired first delivery became successful after restart", r.Report)
	}
}

func TestRetainedUpdateRequiresCurrentAuthorityAndSignedHostArtifact(t *testing.T) {
	for _, invalid := range []string{"controller", "manifest", "signature", "architecture", "cancelled", "missing_artifact"} {
		t.Run(invalid, func(t *testing.T) {
			arch := runtime.GOARCH
			if invalid == "architecture" {
				if arch == "arm64" {
					arch = "amd64"
				} else {
					arch = "arm64"
				}
			}
			release, pub, body := signedUpdate(t, arch)
			path := filepath.Join(t.TempDir(), "installed")
			if err := os.WriteFile(path, body, 0755); err != nil {
				t.Fatal(err)
			}
			var original Record
			dir, _ := legacyJournal(t, func(j *Journal) {
				cfg, _ := j.Config()
				cfg.PublicKey, cfg.ControllerGeneration = pub, 1
				if err := j.SetConfig(cfg); err != nil {
					t.Fatal(err)
				}
				generation := int64(1)
				c := api.ExecutionCommand{Id: uuid.New(), ServerId: cfg.ServerID, Generation: 1, ControllerGeneration: &generation, Kind: "update", Release: &release, PlanHash: execution.Hash(&release), Deadline: time.Now().Add(-time.Hour)}
				if invalid == "signature" {
					release.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
					c.PlanHash = execution.Hash(&release)
				}
				if invalid == "manifest" {
					c.PlanHash = strings.Repeat("e", 64)
				}
				original = Record{Command: c, Report: api.AgentReport{Id: c.Id, Sequence: 2, State: "running"}, Checkpoint: "accepted"}
				if err := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "operations", c.Id.String(), original) }); err != nil {
					t.Fatal(err)
				}
			})
			j, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			command := original.Command
			if invalid == "controller" {
				command.ControllerGeneration = nil
			}
			if invalid == "cancelled" {
				command.CancelRequested = true
			}
			if invalid == "missing_artifact" {
				os.Remove(path)
			}
			e := Engine{Journal: j, VerifyInstalledUpdate: func(r api.AgentRelease) error { return VerifyInstalledUpdate(r, pub, path) }, Update: func(context.Context, api.AgentRelease) error {
				t.Fatal("invalid expired update downloaded or installed")
				return nil
			}}
			_ = e.Execute(context.Background(), command)
			r, _ := j.Record(command.Id)
			if r.Report.State == "succeeded" {
				t.Fatal("invalid retained update was recorded as success")
			}
		})
	}
}

func TestInstalledUpdateCrashHelper(t *testing.T) {
	dir := os.Getenv("ASTILBA_TEST_UPDATE_STATE")
	if dir == "" {
		return
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := j.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatal("pending update missing", err)
	}
	cfg, _ := j.Config()
	path := os.Getenv("ASTILBA_TEST_UPDATE_EXECUTABLE")
	e := Engine{Journal: j, VerifyInstalledUpdate: func(r api.AgentRelease) error { return VerifyInstalledUpdate(r, cfg.PublicKey, path) }, Update: func(ctx context.Context, release api.AgentRelease) error {
		artifact, err := os.Open(os.Getenv("ASTILBA_TEST_UPDATE_ARTIFACT"))
		if err != nil {
			return err
		}
		defer artifact.Close()
		if err = Install(ctx, release, cfg.PublicKey, artifact, path); err != nil {
			return err
		}
		os.Exit(88) // Replacement is durable; no success has been recorded.
		return nil
	}}
	if err = e.Execute(context.Background(), pending[0].Command); err != nil {
		t.Fatal(err)
	}
	t.Fatal("update did not reach the replacement crash point")
}

func TestSignedUpdateReconcilesAfterProcessCrashFollowingReplacement(t *testing.T) {
	j, p := testVisualJournal(t)
	release, pub, body := signedUpdate(t, runtime.GOARCH)
	dir := t.TempDir()
	path, artifact := filepath.Join(dir, "agent"), filepath.Join(dir, "artifact")
	if err := os.WriteFile(path, []byte("previous executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, body, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := j.Config()
	cfg.PublicKey = pub
	if err := j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, Generation: 1, Kind: "update", Release: &release, PlanHash: execution.Hash(&release), Deadline: time.Now().Add(2 * time.Second)}
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	state := j.dir
	j.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInstalledUpdateCrashHelper$")
	cmd.Env = append(os.Environ(), "ASTILBA_TEST_UPDATE_STATE="+state, "ASTILBA_TEST_UPDATE_EXECUTABLE="+path, "ASTILBA_TEST_UPDATE_ARTIFACT="+artifact)
	if out, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState.ExitCode() != 88 {
		t.Fatalf("replacement crash failed: %v %s", err, out)
	}
	if remaining := time.Until(c.Deadline); remaining > 0 {
		time.Sleep(remaining + time.Millisecond)
	}
	j, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	r, err := j.Record(c.Id)
	if err != nil || r.Report.State != "running" || r.AcceptedAt == nil || r.AcceptedAt.After(c.Deadline) {
		t.Fatal("crash lost admitted update provenance or invented success", err)
	}
	e := Engine{Journal: j, VerifyInstalledUpdate: func(r api.AgentRelease) error { return VerifyInstalledUpdate(r, pub, path) }, Update: func(context.Context, api.AgentRelease) error {
		t.Fatal("restart repeated an expired installation")
		return nil
	}}
	if err = e.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	r, _ = j.Record(c.Id)
	if r.Report.State != "succeeded" || r.Command.Id != c.Id || !r.Command.Deadline.Equal(c.Deadline) {
		t.Fatal("same installed artifact did not reconcile after process crash", r.Report)
	}
}

func TestSucceededUpdateAcknowledgesAfterLaterSignedLocalInstall(t *testing.T) {
	j, p := testVisualJournal(t)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	release := func(version string) (api.AgentRelease, []byte) {
		body := []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"Version\":\"%s\",\"Protocol\":1,\"Arch\":\"%s\"}'\n", version, runtime.GOARCH))
		digest := sha256.Sum256(body)
		r := api.AgentRelease{Version: version, Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(body)), Sha256: hex.EncodeToString(digest[:])}
		r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, agentrelease.Message(r)))
		return r, body
	}
	first, firstBody := release("fixture.2")
	next, nextBody := release("fixture.3")
	path := filepath.Join(t.TempDir(), "agent")
	if err = os.WriteFile(path, []byte("previous executable"), 0755); err != nil {
		t.Fatal(err)
	}
	command := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, Generation: 1, Kind: "update", Release: &first, PlanHash: execution.Hash(&first), Deadline: time.Now().Add(time.Hour)}
	var reports atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+p.ServerID.String()+".fixture" {
			t.Error("missing assigned-agent authentication")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/agent/v1/report" {
			var report api.AgentReport
			if json.NewDecoder(r.Body).Decode(&report) != nil || report.Id != command.Id || report.PlanHash != command.PlanHash || report.State != "succeeded" {
				t.Error("historical update report changed")
				w.WriteHeader(400)
				return
			}
			reports.Add(1)
		}
		_, _ = w.Write([]byte(`{"role":"deployment","rotationRequested":false}`))
	}))
	defer server.Close()
	cfg, _ := j.Config()
	cfg.Origin, cfg.PublicKey, cfg.Enrolled, cfg.Credential = server.URL, pub, true, "fixture"
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	e := Engine{Journal: j, Update: func(ctx context.Context, r api.AgentRelease) error {
		return Install(ctx, r, pub, bytes.NewReader(firstBody), path)
	}, VerifyInstalledUpdate: func(r api.AgentRelease) error { return VerifyInstalledUpdate(r, pub, path) }}
	if err = e.Execute(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	before, err := j.Record(command.Id)
	if err != nil || before.Report.State != "succeeded" || before.Acknowledged {
		t.Fatal("signed update did not retain its unacknowledged success", err)
	}
	// update-local permits pending agent updates. Replacing the executable
	// again does not invalidate the first update's durable historical result.
	if err = j.RequireUnreservedHost(); err != nil {
		t.Fatal(err)
	}
	if err = Install(context.Background(), next, pub, bytes.NewReader(nextBody), path); err != nil {
		t.Fatal(err)
	}
	if VerifyInstalledUpdate(first, pub, path) == nil || VerifyInstalledUpdate(next, pub, path) != nil {
		t.Fatal("later signed artifact was not installed")
	}
	c := NewClient(j, &e)
	c.HTTP, c.Observe = server.Client(), func(context.Context) api.AgentHeartbeat { return api.AgentHeartbeat{} }
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	_ = c.Run(ctx)
	after, err := j.Record(command.Id)
	if err != nil || reports.Load() == 0 || !after.Acknowledged || deployment.Hash(after.Report) != deployment.Hash(before.Report) {
		t.Fatal("historical success could not report after a signed local replacement", reports.Load(), after, err)
	}
	if err = VerifyInstalledUpdate(next, pub, path); err != nil {
		t.Fatal("reporting required executable rollback", err)
	}
	if _, err = j.Accept(visualCommand(p, "observe", 2)); err != nil {
		t.Fatal("acknowledged update still blocked ordinary work", err)
	}
}
