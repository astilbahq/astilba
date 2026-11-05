package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func recoveryFixtureRestriction(t *testing.T, ctx context.Context, st *store.Store, server uuid.UUID) api.ExecutionCommand {
	t.Helper()
	var raw []byte
	if err := st.Pool.QueryRow(ctx, `SELECT installed_manifest FROM network_members WHERE server_id=$1`, server).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var baseline hostnetwork.Manifest
	if json.Unmarshal(raw, &baseline) != nil {
		t.Fatal("invalid queued-removal baseline")
	}
	wanted := baseline
	wanted.Generation++
	wanted.Exports, wanted.Egress = nil, nil
	body := hostnetwork.Command{Action: "restrict", NetworkID: baseline.NetworkID, MembershipID: baseline.MembershipID, Baseline: &baseline, Manifest: &wanted}
	payload, _ := stablejson.Marshal(body)
	message := jsontext.Value(payload)
	return api.ExecutionCommand{Id: uuid.New(), ServerId: server, Generation: 10000, Kind: "network", Action: "inspect", ResourceId: baseline.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(time.Hour), Network: &message}
}
