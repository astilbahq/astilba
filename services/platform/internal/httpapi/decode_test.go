package httpapi

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
	crosshost := jsontext.Value(`{"phase":"a && b <c>"}`)
	want := api.AgentReport{Id: uuid.New(), Generation: 3, PlanHash: "hash", Sequence: 2, State: "running", Note: "a && b <c>", Crosshost: &crosshost}
	raw, err := stablejson.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decode[api.AgentReport](httptest.NewRequest("POST", "/", strings.NewReader(string(raw))))
	if err != nil {
		t.Fatal(err)
	}
	written, _ := stablejson.Marshal(crosshost)
	if string(*got.Crosshost) != string(written) {
		t.Fatalf("raw payload was rewritten: %s", *got.Crosshost)
	}
	got.Crosshost, want.Crosshost = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// PostgreSQL cannot store NUL, and its refusal aborted the write's transaction with a 500. A
// person's or program's body is refused on the member that holds it.
func TestDecodeRefusesNULOnItsMember(t *testing.T) {
	for field, body := range map[string]string{
		"name":            `{"name":"a` + `\` + `u0000"}`,
		"steps.detail":    `{"id":"` + uuid.NewString() + `","steps":[{"name":"a","detail":"` + `\` + `u0000","state":"done"}]}`,
		"crosshost.phase": `{"id":"` + uuid.NewString() + `","crosshost":{"phase":"` + `\` + `u0000"}}`,
		"capabilities.os": `{"capabilities":{"os":"linux` + `\` + `u0000"}}`,
	} {
		t.Run(field, func(t *testing.T) {
			var err error
			switch field {
			case "name":
				_, err = decode[api.Name](httptest.NewRequest("POST", "/", strings.NewReader(body)))
			case "capabilities.os":
				_, err = decode[api.AgentHeartbeat](httptest.NewRequest("POST", "/", strings.NewReader(body)))
			default:
				_, err = decode[api.AgentReport](httptest.NewRequest("POST", "/", strings.NewReader(body)))
			}
			refused, ok := err.(*domain.Error)
			if !ok || refused.Code != "invalid_input" || !slices.Equal(refused.Fields, []string{field}) {
				t.Fatalf("answered %v", err)
			}
		})
	}
}

// An agent's strings carry its host's output, which the host's operator does not choose, so a
// NUL there is replaced rather than refused: refusing it would refuse every heartbeat and
// report after it.
func TestDecodeReplacesNULFromAnAgent(t *testing.T) {
	escape := `\` + `u0000`
	report := `{"id":"` + uuid.NewString() + `","generation":1,"planHash":"h","sequence":1,"state":"running","note":"a` + escape + `","steps":[{"name":"pull` + escape + `","detail":"` + escape + `b","state":"done"}],"crosshost":{"phase":"` + escape + `"}}`
	agent := httptest.NewRequest("POST", "/agent/v1/report", strings.NewReader(report))
	agent = agent.WithContext(context.WithValue(agent.Context(), agentKey, execution.Identity{ID: uuid.New()}))
	got, err := decode[api.AgentReport](agent)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "a " || got.Steps[0].Name != "pull " || got.Steps[0].Detail != " b" || string(*got.Crosshost) != `{"phase":"`+`\`+`u0020"}` {
		t.Fatalf("decoded %+v, %s", got, *got.Crosshost)
	}
	// Enrollment comes before the agent has a credential, and reports the same host output.
	enroll := httptest.NewRequest("POST", "/agent/v1/enroll", strings.NewReader(`{"serverId":"`+uuid.NewString()+`","token":"t","machineId":"`+uuid.NewString()+`","capabilities":{"protocol":1,"version":"v","os":"linux","arch":"arm64","docker":true},"preflight":{"ready":true,"osRelease":"ubuntu`+escape+`","dockerVersion":"27`+escape+`","freeBytes":1,"notes":["`+escape+`"]}}`))
	enrolled, err := decode[api.AgentEnrollment](enroll)
	if err != nil {
		t.Fatal(err)
	}
	if enrolled.Preflight.OsRelease != "ubuntu " || enrolled.Preflight.DockerVersion != "27 " || enrolled.Preflight.Notes[0] != " " {
		t.Fatalf("decoded %+v", enrolled.Preflight)
	}
}

// Path and query values reach queries as text too, so the escape that spells NUL is refused
// before any handler runs.
func TestRequestsRefuseNULInTheirURL(t *testing.T) {
	s := &Server{Log: slog.New(slog.DiscardHandler)}
	reached := false
	handler := s.request(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	for target, field := range map[string]string{
		"/api/v1/workspaces/a%00b":           "path",
		"/api/v1/workspaces?filter=a%00":     "query",
		"/api/v1/workspaces?a%00=b":          "query",
		"/api/v1/workspaces?filter=%2500":    "",
		"/api/v1/workspaces/a%2500?filter=b": "",
	} {
		reached = false
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if field == "" {
			if !reached {
				t.Errorf("%s was refused: %s", target, w.Body)
			}
			continue
		}
		var refused api.Error
		if reached || w.Code != 400 || json.Unmarshal(w.Body.Bytes(), &refused) != nil || refused.Fields == nil || !slices.Equal(*refused.Fields, []string{field}) {
			t.Errorf("%s: %d %s", target, w.Code, w.Body)
		}
	}
}
