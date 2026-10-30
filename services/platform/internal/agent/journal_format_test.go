package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func legacyJournal(t *testing.T, seed func(*Journal)) (string, []byte) {
	t.Helper()
	j, _ := testVisualJournal(t)
	if seed != nil {
		seed(j)
	}
	raw, err := json.Marshal(j.keys)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(j.dir, "keyring.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return j.dir, raw
}

func encryptedJournalRows(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	db, err := bolt.Open(filepath.Join(dir, "journal.db"), 0600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string][]byte{}
	if err = db.View(func(tx *bolt.Tx) error {
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

func TestLocalStateMigrationPreservesKeysCiphertextAndRecoveryEvidence(t *testing.T) {
	var expected Config
	var capsule deployment.RecoveryDescriptor
	capsuleBody := []byte("opaque-controller-recovery-capsule")
	dir, legacy := legacyJournal(t, func(j *Journal) {
		expected, _ = j.Config()
		expected.MachineID = uuid.New()
		retired, err := cryptobox.New("retired")
		if err != nil {
			t.Fatal(err)
		}
		j.keys.Keys["retired"] = retired.Keys["retired"]
		j.keys.Active = "retired"
		if err = j.SetConfig(expected); err != nil {
			t.Fatal(err)
		}
		parent, id, env, unit, group := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
		capsule = deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: parent, RootHash: strings.Repeat("a", 64), GroupID: group, GroupHash: strings.Repeat("b", 64), BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(capsuleBody), Bytes: len(capsuleBody), Participants: []uuid.UUID{expected.ServerID}}
		body := deployment.CrossHostCommand{ParentID: parent, GroupHash: capsule.GroupHash, Phase: "checkpoint", Unit: deployment.DistributedUnit{Plan: deployment.Plan{ServerID: expected.ServerID, EnvironmentID: env, UnitID: unit, Local: &deployment.LocalPlacement{Primary: true, GroupID: group}}.WithHash()}}
		rawBody, _ := json.Marshal(body)
		message := json.RawMessage(rawBody)
		header := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: expected.ServerID, EnvironmentId: env, ResourceId: unit, Kind: "crosshost", PlanHash: body.Digest(), Crosshost: &message}
		if err := j.retainCheckpoint(header, capsule, capsuleBody); err != nil {
			t.Fatal(err)
		}
		if err := j.db.Update(func(tx *bolt.Tx) error {
			return j.put(tx, "operations", header.Id.String(), Record{Command: header, Report: api.AgentReport{Id: header.Id, State: "succeeded"}, Acknowledged: true})
		}); err != nil {
			t.Fatal(err)
		}
		j.keys.Active = "host"
		// Idle distributed runtimes retain a compatibility sentinel, which the
		// existing update admission excludes from pending workload commands.
		sentinelID, sentinelUnit := uuid.New(), uuid.New()
		if err := j.db.Update(func(tx *bolt.Tx) error {
			if err := j.put(tx, "distributed-fences", sentinelID.String(), distributedFence{UnitID: sentinelUnit}); err != nil {
				return err
			}
			return j.put(tx, "operations", sentinelID.String(), Record{Command: api.ExecutionCommand{Id: sentinelID, Kind: "visual"}, Report: api.AgentReport{State: "needs_attention"}, Checkpoint: "distributed-runtime-fence"})
		}); err != nil {
			t.Fatal(err)
		}
	})
	before := encryptedJournalRows(t, dir)
	for range 2 {
		j, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		keys, _ := cryptobox.Parse(legacy)
		if deployment.Hash(j.keys) != deployment.Hash(keys) {
			t.Fatal("migration changed active or retained key bytes")
		}
		cfg, err := j.Config()
		if err != nil || deployment.Hash(cfg) != deployment.Hash(expected) {
			t.Fatal("migration lost a record encrypted by a retained key", err)
		}
		export := filepath.Join(t.TempDir(), "recovery")
		if err = j.ExportRecovery(export); err != nil {
			t.Fatal("migrated state lost offline recovery export", err)
		}
		var evidence deployment.RecoveryEvidence
		host, err := os.ReadFile(filepath.Join(export, "host.json"))
		if err != nil || json.Unmarshal(host, &evidence) != nil || evidence.Version != 1 || evidence.ServerID != expected.ServerID {
			t.Fatal("migration changed the recovery evidence format", err)
		}
		exportedCapsule, err := os.ReadFile(filepath.Join(export, capsule.CheckpointID.String()+".capsule"))
		proofs, proofErr := os.ReadFile(filepath.Join(export, "proofs.jsonl"))
		if err != nil || !bytes.Equal(exportedCapsule, capsuleBody) || proofErr != nil || deployment.RecoveryDigest(proofs) != evidence.ProofDigest || len(evidence.Roots) != 1 || evidence.Roots[0].Descriptor.Digest != capsule.Digest {
			t.Fatal("migration lost retained capsules or command evidence", err, proofErr)
		}
		j.Close()
		after := encryptedJournalRows(t, dir)
		if deployment.Hash(before) != deployment.Hash(after) {
			t.Fatal("migration or restart rewrote encrypted journal records")
		}
	}
}

func TestLocalStateMigrationRefusesUnfinishedLegacyWorkWithoutWrites(t *testing.T) {
	cases := map[string]func(*Journal){
		"reservation": func(j *Journal) {
			err := j.db.Update(func(tx *bolt.Tx) error {
				return j.put(tx, "cross-host", "active", crossHostState{ParentID: uuid.New()})
			})
			if err != nil {
				t.Fatal(err)
			}
		},
		"controller_hold": func(j *Journal) {
			a := recoveredAuthority{}
			a.Handover.Authority.ParentID = uuid.New()
			if err := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "recovery-authority", "current", a) }); err != nil {
				t.Fatal(err)
			}
		},
		"database_upgrade": func(j *Journal) {
			if err := j.saveUpgrade(upgradeState{Command: upgrade.Command{Plan: upgrade.Plan{ID: uuid.New()}}, Status: upgrade.Status{Phase: "running"}}); err != nil {
				t.Fatal(err)
			}
		},
		"paused_schedule": func(j *Journal) {
			if err := j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: backup.Policy{ID: uuid.New()}}, Writers: []string{"writer"}}); err != nil {
				t.Fatal(err)
			}
		},
		"uncertain_advanced_schedule": func(j *Journal) {
			if err := j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: backup.Policy{ID: uuid.New(), Coordinated: true}}, Runs: []backup.Run{{State: "needs_attention"}}}); err != nil {
				t.Fatal(err)
			}
		},
		"unacknowledged_terminal_command": func(j *Journal) {
			id := uuid.New()
			if err := j.db.Update(func(tx *bolt.Tx) error {
				return j.put(tx, "operations", id.String(), Record{Command: api.ExecutionCommand{Id: id, Kind: "visual"}, Report: api.AgentReport{State: "succeeded"}})
			}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			dir, key := legacyJournal(t, seed)
			before, err := os.ReadFile(filepath.Join(dir, "journal.db"))
			if err != nil {
				t.Fatal(err)
			}
			if j, err := Open(dir); err == nil {
				j.Close()
				t.Fatal("unfinished legacy work was migrated")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "journal.db"))
			gotKey, _ := os.ReadFile(filepath.Join(dir, "keyring.json"))
			if !bytes.Equal(before, after) || !bytes.Equal(key, gotKey) {
				t.Fatal("refusal changed legacy state or keys")
			}
		})
	}
}

func TestLocalStateMigrationCrashHelper(t *testing.T) {
	dir, point := os.Getenv("ASTILBA_TEST_MIGRATION_DIR"), os.Getenv("ASTILBA_TEST_MIGRATION_POINT")
	if dir == "" {
		return
	}
	_, err := openJournal(dir, func(at string) error {
		if at == point {
			os.Exit(87)
		}
		return nil
	})
	t.Fatalf("migration did not crash at %s: %v", point, err)
}

func TestLocalStateMigrationSurvivesProcessCrash(t *testing.T) {
	for _, point := range []string{"local-state-written", "local-state-synced", "local-state-renamed", "local-state-directory-synced"} {
		t.Run(point, func(t *testing.T) {
			dir, legacy := legacyJournal(t, nil)
			before := encryptedJournalRows(t, dir)
			cmd := exec.Command(os.Args[0], "-test.run=^TestLocalStateMigrationCrashHelper$")
			cmd.Env = append(os.Environ(), "ASTILBA_TEST_MIGRATION_DIR="+dir, "ASTILBA_TEST_MIGRATION_POINT="+point)
			if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 87 {
				t.Fatal("expected injected process exit", err)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "keyring.json"))
			if point == "local-state-written" || point == "local-state-synced" {
				if !bytes.Equal(raw, legacy) {
					t.Fatal("pre-rename crash replaced legacy keys")
				}
			} else if _, err := cryptobox.Parse(raw); err == nil {
				t.Fatal("post-rename state retained the legacy downgrade path")
			}
			j, err := Open(dir)
			if err != nil {
				t.Fatal("crashed migration could not reopen", err)
			}
			j.Close()
			if deployment.Hash(before) != deployment.Hash(encryptedJournalRows(t, dir)) {
				t.Fatal("crash/restart changed ciphertext")
			}
		})
	}
}

func TestLocalStateRejectsUnknownVersionAndMissingKeys(t *testing.T) {
	for _, invalid := range []string{`{"agentStateVersion":99,"keyring":{}}`, ""} {
		dir, _ := legacyJournal(t, nil)
		path := filepath.Join(dir, "keyring.json")
		if invalid == "" {
			os.Remove(path)
		} else if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(dir, "journal.db"))
		if j, err := Open(dir); err == nil {
			j.Close()
			t.Fatal("unsupported state opened")
		}
		after, _ := os.ReadFile(filepath.Join(dir, "journal.db"))
		if !bytes.Equal(before, after) {
			t.Fatal("unsupported state initialized or pruned the journal")
		}
		if invalid == "" {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("missing keys were replaced")
			}
		}
	}
}

func TestPreviousAgentRejectsMigratedLocalStateBeforeJournalWrites(t *testing.T) {
	previous := os.Getenv("PLAT_AGENT_PREVIOUS_BINARY")
	if previous == "" {
		t.Skip("requires the actual previous agent binary")
	}
	dir, _ := legacyJournal(t, nil)
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A valid encrypted bucket absent in the previous journal schema makes
	// accidental initialization observable as well as checking all file bytes.
	if err = j.db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("recovery-gc")) }); err != nil {
		t.Fatal(err)
	}
	j.Close()
	before, _ := os.ReadFile(filepath.Join(dir, "journal.db"))
	key, _ := os.ReadFile(filepath.Join(dir, "keyring.json"))
	out, err := exec.Command(previous, "inspect-upgrades", "--state", dir).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "secret key is unavailable") {
		t.Fatalf("previous binary did not fail at key loading: %v %s", err, out)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "journal.db"))
	gotKey, _ := os.ReadFile(filepath.Join(dir, "keyring.json"))
	if !bytes.Equal(before, after) || !bytes.Equal(key, gotKey) {
		t.Fatal("previous binary wrote migrated state before refusing it")
	}
}

func TestLocalStateFirstOpenSurvivesProcessCrash(t *testing.T) {
	for _, point := range []string{"local-state-written", "local-state-synced", "local-state-renamed", "local-state-directory-synced"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestLocalStateMigrationCrashHelper$")
			cmd.Env = append(os.Environ(), "ASTILBA_TEST_MIGRATION_DIR="+dir, "ASTILBA_TEST_MIGRATION_POINT="+point)
			if out, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState.ExitCode() != 87 {
				t.Fatalf("first-open crash failed: %v %s", err, out)
			}
			keyBefore, _ := os.ReadFile(filepath.Join(dir, "keyring.json"))
			j, err := Open(dir)
			if err != nil {
				t.Fatal("interrupted initial state could not reopen", err)
			}
			if _, err = j.Config(); !os.IsNotExist(err) {
				t.Fatal("interrupted first open invented an identity", err)
			}
			j.Close()
			if len(keyBefore) > 0 {
				keyAfter, _ := os.ReadFile(filepath.Join(dir, "keyring.json"))
				if !bytes.Equal(keyBefore, keyAfter) {
					t.Fatal("first-open restart replaced durable keys")
				}
			}
			// Initialization has now created buckets, so losing the key must
			// still fail closed instead of minting an unreadable replacement.
			if err = os.Remove(filepath.Join(dir, "keyring.json")); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "journal.db"))
			if opened, err := Open(dir); err == nil {
				opened.Close()
				t.Fatal("initialized empty journal recreated missing keys")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "journal.db"))
			if !bytes.Equal(before, after) {
				t.Fatal("missing-key refusal wrote initialized state")
			}
		})
	}
}
