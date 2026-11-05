package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

func TestDecodeRefusesBodiesThatReadDifferentlyElsewhere(t *testing.T) {
	for name, body := range map[string]string{
		"case variant after the name": `{"role":"viewer","ROLE":"owner"}`,
		"case variant alone":          `{"ROLE":"owner"}`,
		"duplicate":                   `{"role":"viewer","role":"owner"}`,
		"duplicate through an escape": `{"role":"viewer","r` + `\` + "u006fle\":\"owner\"}",
		"invalid UTF-8":               "{\"role\":\"view\xffer\"}",
		"lone surrogate":              `{"role":"view` + `\` + "ud800er\"}",
		"unknown member":              `{"role":"viewer","extra":true}`,
		"trailing value":              `{"role":"viewer"} {"role":"owner"}`,
		"empty":                       ``,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := decode[api.RoleChange](httptest.NewRequest("PUT", "/", strings.NewReader(body))); err == nil {
				t.Fatalf("accepted %q as %+v", body, got)
			}
		})
	}
	got, err := decode[api.RoleChange](httptest.NewRequest("PUT", "/", strings.NewReader(`{"role":"viewer"}`)))
	if err != nil || got.Role != "viewer" {
		t.Fatalf("exact body: %+v, %v", got, err)
	}
}

// Freeform schema objects reach handlers as raw bytes, so the outer decode is the
// only place that sees their duplicate names.
func TestDecodeRefusesDuplicatesInsideRawPayloads(t *testing.T) {
	body := `{"id":"` + uuid.NewString() + `","crosshost":{"planHash":"a","planHash":"b"}}`
	if got, err := decode[api.AgentReport](httptest.NewRequest("POST", "/", strings.NewReader(body))); err == nil {
		t.Fatalf("accepted %+v", got)
	}
}

// An agent on an older release still writes reports with encoding/json v1.
func TestDecodeReadsReportsFromOlderAgents(t *testing.T) {
	crosshost := json.RawMessage(`{"phase":"a && b <c>"}`)
	want := api.AgentReport{Id: uuid.New(), Generation: 3, PlanHash: "hash", Sequence: 2, State: "running", Note: "a && b <c>", Crosshost: &crosshost}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decode[api.AgentReport](httptest.NewRequest("POST", "/", strings.NewReader(string(raw))))
	if err != nil {
		t.Fatal(err)
	}
	written, _ := json.Marshal(crosshost)
	if string(*got.Crosshost) != string(written) {
		t.Fatalf("raw payload was rewritten: %s", *got.Crosshost)
	}
	got.Crosshost, want.Crosshost = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
