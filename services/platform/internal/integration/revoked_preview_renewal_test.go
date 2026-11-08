package integration

import (
	"encoding/json/v2"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

// Revocation released the host's previews without anything confirming their removal, so renewing
// it must not let it carry on beside unmanaged copies of them. An ordinary server's renewal still
// needs the same machine.
func TestRenewingARevokedPreviewHostNeedsACleanHost(t *testing.T) {
	h := newHungPreview(t)
	ordinary, ordinaryInput, _ := enrollment(t, h.f, h.b, h.ws, "deployment")
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/enroll", "", ordinaryInput, 200, nil)
	renew := func(server uuid.UUID, in api.AgentEnrollment) api.AgentEnrollment {
		t.Helper()
		h.b.call("POST", h.prefix+"/servers/"+server.String()+"/revoke", nil, 204, nil)
		var renewed api.Enrollment
		h.b.call("POST", h.prefix+"/servers/"+server.String()+"/enrollment", nil, 201, &renewed)
		in.Token = renewed.Token
		in.Credential, _ = cryptobox.Token()
		return in
	}

	preview := renew(h.server, h.enrolled)
	preview.Preflight.ExistingContainers = 1
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/enroll", "", preview, 400, nil)
	preview.Preflight.ExistingContainers = 0
	preview.MachineId = uuid.New()
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/enroll", "", preview, 200, nil)

	in := renew(ordinary.Server.Id, ordinaryInput)
	other := in
	other.MachineId = uuid.New()
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/enroll", "", other, 401, nil)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
}

// A cleanup the revoked host finished but never reported comes back as a receipt when it enrolls
// with a restored management. The revocation already released that preview, and nothing the host
// says may change what the owner was told.
func TestRevokedPreviewHostCannotConfirmARemovalLater(t *testing.T) {
	h := newHungPreview(t)
	ctx := t.Context()
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	preview := readyPreview(t, h)
	h.b.call("POST", h.path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": "cleanup", "head": preview.Head, "headRepository": preview.HeadRepository}, 200, nil)
	waitPreviewAt(t, h, h.path, "deleting")
	cleanup := nextPreviewCommand(t, h)

	h.b.call("POST", h.prefix+"/servers/"+h.server.String()+"/revoke", nil, 204, nil)
	released := previewAt(t, h, h.path)
	if released.State != "deleted" || released.Note == "" {
		t.Fatal("revocation didn't release the preview", released.State, released.Note)
	}
	runtime := func() map[string]string {
		t.Helper()
		rows, err := h.f.st.Pool.Query(ctx, "SELECT r.unit_id,p.plan FROM visual_runtime r JOIN visual_plans p ON p.id=r.plan_id WHERE r.workspace_id=$1 AND r.server_id=$2", h.ws, h.server)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		units := map[string]string{}
		for rows.Next() {
			var unit uuid.UUID
			var raw []byte
			var plan deployment.Plan
			if err = rows.Scan(&unit, &raw); err != nil || json.Unmarshal(raw, &plan) != nil {
				t.Fatal("runtime row", err)
			}
			units[unit.String()] = plan.PrimaryPlan().Hash
		}
		return units
	}
	units := runtime()
	if len(units) != 1 {
		t.Fatal("the released preview's runtime record", units)
	}

	if err := h.f.st.PrepareRecovery(ctx, h.server, 1); err != nil {
		t.Fatal(err)
	}
	var ticket api.TransferTicket
	h.b.call("POST", h.prefix+"/servers/"+h.server.String()+"/transfer", nil, 201, &ticket)
	in := h.enrolled
	in.Credential, _ = cryptobox.Token()
	in.Token, in.ControllerGeneration = ticket.Token, &ticket.Generation
	receipts := []api.PreviewCleanupReceipt{{OperationId: cleanup.Id, Digest: cleanup.PlanHash}}
	in.Inventory = &api.ControllerInventory{Units: units, Schedules: map[string]int64{}, PreviewCleanups: &receipts}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/enroll", "", in, 200, nil)

	if after := previewAt(t, h, h.path); after.State != released.State || after.Note != released.Note {
		t.Fatal("the revoked host's receipt changed the released preview", after.State, after.Note)
	}
	var outcome *string
	if err := h.f.st.Pool.QueryRow(ctx, "SELECT runtime_outcome FROM execution_operations WHERE id=$1", cleanup.Id).Scan(&outcome); err != nil || outcome != nil {
		t.Fatal("the receipt recorded a removal nobody can confirm", outcome, err)
	}
	if got := runtime(); len(got) != len(units) {
		t.Fatal("the receipt retired management's runtime record", got)
	}
}
