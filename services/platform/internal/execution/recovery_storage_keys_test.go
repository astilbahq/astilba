package execution

import (
	"encoding/json/v2"
	"slices"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

func keyRow(state string, handedOver any, extra ...string) portability.Row {
	row := portability.Row{}
	recoverySet(row, "state", state)
	recoverySet(row, "handed_over", handedOver)
	for i := 0; i+1 < len(extra); i += 2 {
		recoverySet(row, extra[i], extra[i+1])
	}
	return row
}

// A storage key's copy further along wins whole, since its columns hold together, and its
// handover is the later revocation's, whichever copy holds it.
func TestRecoveryKeepsTheKeyFurtherAlong(t *testing.T) {
	cases := []struct {
		prior, incoming portability.Row
		state, from     string
		handedOver      string
	}{
		{keyRow("active", nil, "from", "prior"), keyRow("revoked", nil, "from", "incoming"), "revoked", "incoming", "null"},
		{keyRow("revoking", 2, "from", "prior"), keyRow("active", 3, "from", "incoming"), "revoking", "prior", "3"},
		{keyRow("active", 4, "from", "prior"), keyRow("active", nil, "from", "incoming"), "active", "prior", "4"},
		{keyRow("minting", nil, "from", "prior"), keyRow("active", 1, "from", "incoming"), "active", "incoming", "1"},
	}
	for _, c := range cases {
		got, err := recoveryKeyKnowledge(c.prior, c.incoming)
		if err != nil {
			t.Fatal(err)
		}
		var state, from string
		_ = json.Unmarshal(got["state"], &state)
		_ = json.Unmarshal(got["from"], &from)
		if state != c.state || from != c.from || string(got["handed_over"]) != c.handedOver {
			t.Errorf("merged %s with %s: %s from %s, handed over %s", c.prior["state"], c.incoming["state"], state, from, got["handed_over"])
		}
	}
	if _, err := recoveryKeyKnowledge(keyRow("active", nil), keyRow("lost", nil)); err == nil {
		t.Error("an unknown state was merged")
	}
}

// A storage account keeps the newer credential and every peer either copy lists.
func TestRecoveryKeepsAnAccountsNewerCredentialAndAllItsPeers(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	account := func(generation int64, credential string, peers ...uuid.UUID) portability.Row {
		row := portability.Row{}
		recoverySet(row, "generation", generation)
		recoverySet(row, "encrypted_credential", credential)
		recoverySet(row, "peers", append([]uuid.UUID{}, peers...))
		return row
	}
	got, err := recoveryAccountKnowledge(account(2, "older", a), account(3, "newer", b, a))
	if err != nil {
		t.Fatal(err)
	}
	var peers []uuid.UUID
	_ = json.Unmarshal(got["peers"], &peers)
	if string(got["encrypted_credential"]) != `"newer"` || !slices.Equal(peers, []uuid.UUID{a, b}) {
		t.Fatalf("merged %s with peers %v", got["encrypted_credential"], peers)
	}
	if got, _ = recoveryAccountKnowledge(account(3, "kept", c), account(1, "stale")); string(got["encrypted_credential"]) != `"kept"` {
		t.Fatalf("an older credential replaced a newer one: %s", got["encrypted_credential"])
	}
}
