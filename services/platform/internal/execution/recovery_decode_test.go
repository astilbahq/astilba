package execution

import "testing"

// Recovery bundles are operator-supplied files that no API validator sees.
func TestRecoveryDecodeRefusesAmbiguousNames(t *testing.T) {
	type record struct {
		ServerID string `json:"serverId"`
	}
	for _, raw := range []string{
		`{"serverId":"a","SERVERID":"b"}`,
		`{"serverId":"a","serverId":"b"}`,
		`{"serverId":"a"} {}`,
		`{"serverId":"a","other":1}`,
	} {
		var got record
		if err := recoveryDecode([]byte(raw), &got); err == nil {
			t.Fatalf("accepted %s as %+v", raw, got)
		}
	}
	var got record
	if err := recoveryDecode([]byte(`{"serverId":"a"}`), &got); err != nil || got.ServerID != "a" {
		t.Fatalf("exact record: %+v, %v", got, err)
	}
}
