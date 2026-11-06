package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// An unfinished record stops controller transfer, the way out of a generation conflict, so a
// refusal has to leave the host free to take it, and tell management not to send it again.
func TestRefusedNetworkCommandLeavesTransferAvailable(t *testing.T) {
	j, p := testVisualJournal(t)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, Generation: 1, Kind: "network", Action: "inspect", ResourceId: uuid.New(), PlanHash: strings.Repeat("a", 64)}
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	token := strings.Repeat("t", 43)
	if err := j.Transfer("https://new.example.test", key, 1, token); err == nil {
		t.Fatal("transfer ran past an unfinished command")
	}
	identity := hostnetwork.Identity{MembershipID: uuid.New()}
	if err := (&Engine{Journal: j}).refuseNetwork(context.Background(), c.Id, hostnetwork.Receipt{Identity: identity, NetworkID: c.ResourceId}, "refused"); err != nil {
		t.Fatal(err)
	}
	r, err := j.Record(c.Id)
	if err != nil {
		t.Fatal(err)
	}
	var receipt hostnetwork.Receipt
	if r.Report.State != "failed" || r.Report.Network == nil || hostnetwork.Decode(*r.Report.Network, &receipt) != nil || !receipt.Refused {
		t.Fatalf("report = %+v", r.Report)
	}
	if err = j.Transfer("https://new.example.test", key, 1, token); err != nil {
		t.Fatal("a refused command held the host", err)
	}
}
