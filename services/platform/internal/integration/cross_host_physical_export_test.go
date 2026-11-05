package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func copyRecoveryExport(t *testing.T, source string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "altered")
	if err := os.Mkdir(dst, 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func alterRecoveryCompletion(t *testing.T, source string, parent uuid.UUID, outcome string) string {
	t.Helper()
	dst := copyRecoveryExport(t, source)
	raw, err := os.ReadFile(filepath.Join(dst, "proofs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var changed bool
	var result bytes.Buffer
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var proof deployment.RecoveryProof
		if err := json.Unmarshal(line, &proof); err != nil {
			t.Fatal(err)
		}
		var report api.AgentReport
		if err := json.Unmarshal(proof.Report, &report); err != nil {
			t.Fatal(err)
		}
		if proof.ParentID == parent && report.Crosshost != nil {
			var receipt deployment.CrossHostReceipt
			if err := json.Unmarshal(*report.Crosshost, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Phase == "complete" {
				changed = true
				if outcome == "missing" {
					continue
				}
				report.State = api.ExecutionState(outcome)
				proof.Report, _ = stablejson.Marshal(report)
				line, _ = stablejson.Marshal(proof)
			}
		}
		result.Write(line)
		result.WriteByte('\n')
	}
	if !changed {
		t.Fatal("missing actual complete proof to alter")
	}
	if err := os.WriteFile(filepath.Join(dst, "proofs.jsonl"), result.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(dst, "host.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence deployment.RecoveryEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(result.Bytes())
	evidence.ProofDigest = hex.EncodeToString(digest[:])
	raw, _ = stablejson.Marshal(evidence)
	if err := os.WriteFile(filepath.Join(dst, "host.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return dst
}
