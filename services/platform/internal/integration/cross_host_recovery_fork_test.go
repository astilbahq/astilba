package integration

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

// recoverWithoutAnotherManagementsHosts expects recovery to refuse the hosts of othersExports,
// once they report running root as a reserved host does, because another management started it,
// naming the root and each of them. It then revokes them in the restored management, leaves their
// exports out as the refusal says, and returns the preview of recovering kept's work, which each
// remaining host's export takes over.
func recoverWithoutAnotherManagementsHosts(t *testing.T, ctx context.Context, destination *fixture, input execution.RecoveryImport, root, kept uuid.UUID, others []api.AgentEnrollment, othersExports, remaining []string) (execution.RecoveryImport, execution.RecoveryPreview) {
	t.Helper()
	// The fixture's hosts never reserve, so their exports are made to say they did.
	input.HostExports = slices.Clone(remaining)
	for _, dir := range othersExports {
		active := copyRecoveryExport(t, dir)
		var evidence deployment.RecoveryEvidence
		raw, err := os.ReadFile(filepath.Join(active, "host.json"))
		if err != nil || json.Unmarshal(raw, &evidence) != nil {
			t.Fatal("other management's host export", err)
		}
		evidence.ActiveParent = root
		if err = os.WriteFile(filepath.Join(active, "host.json"), claimBytes(t, evidence), 0o600); err != nil {
			t.Fatal(err)
		}
		input.HostExports = append(input.HostExports, active)
	}
	_, err := destination.execution.RecoverCrossHost(ctx, input)
	if err == nil || !strings.Contains(err.Error(), root.String()) || !strings.Contains(err.Error(), "doesn't descend from") {
		t.Fatal("hosts running another management's root were not refused", err)
	}
	for _, e := range others {
		if !strings.Contains(err.Error(), e.ServerId.String()) {
			t.Fatal("the refusal didn't name a host running the other root", err)
		}
		for _, statement := range []string{`UPDATE servers SET revoked=true,credential_hash=NULL WHERE id=$1`, `UPDATE network_members SET enabled=false WHERE server_id=$1`} {
			if _, err := destination.st.Pool.Exec(ctx, statement, e.ServerId); err != nil {
				t.Fatal(err)
			}
		}
	}
	input.HostExports = remaining
	preview, err := destination.execution.RecoverCrossHost(ctx, input)
	if err != nil || !slices.Contains(preview.Parents, kept) || len(preview.Handovers) != len(remaining) {
		t.Fatal("recovery without the other management's hosts", err, preview)
	}
	for _, handover := range preview.Handovers {
		if handover.Authority.ParentID != kept {
			t.Fatal("a remaining host took over work other than this management's", preview.Handovers)
		}
	}
	return input, preview
}
