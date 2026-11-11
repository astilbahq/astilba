package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// credentialController behaves like management's credential exchange for one server: enrollment
// takes an unspent code, or retries with the credential it stored, and only from the machine that
// first enrolled; rotation needs the current credential and, from a controller that reads it, that
// machine. It reads requests with the schema of the controller version it plays, refusing what that
// version doesn't know.
type credentialController struct {
	machineRotation  atomic.Bool
	mu               sync.Mutex
	credential       string
	machine          uuid.UUID
	codes            map[string]bool
	rotationsWithout int
	advertised       int
}

func (m *credentialController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	modern := m.machineRotation.Load()
	answer := func(status int) {
		w.Header().Set("Astilba-Agent-Features", "none")
		if modern {
			w.Header().Set("Astilba-Agent-Machine-Rotation", "v1")
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"role":"deployment","rotationRequested":false}`))
	}
	var body map[string]jsontext.Value
	if r.Method == "POST" && json.UnmarshalRead(r.Body, &body) != nil {
		answer(400)
		return
	}
	var capabilities map[string]jsontext.Value
	if raw, ok := body["capabilities"]; ok && json.Unmarshal(raw, &capabilities) != nil {
		answer(400)
		return
	}
	if _, present := capabilities["machineBoundRotation"]; present {
		if !modern {
			// Earlier controllers reject members they don't know.
			answer(400)
			return
		}
		m.advertised++
	}
	presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	_, presented, _ = strings.Cut(presented, ".")
	text := func(name string) string {
		var v string
		_ = json.Unmarshal(body[name], &v)
		return v
	}
	switch r.URL.Path {
	case "/agent/v1/enroll":
		token, credential := text("token"), text("credential")
		var machine uuid.UUID
		_ = json.Unmarshal(body["machineId"], &machine)
		unspent := m.codes[token] && m.credential == ""
		if (m.machine != uuid.Nil && machine != m.machine) || (!unspent && (m.credential == "" || credential != m.credential)) {
			answer(401)
			return
		}
		m.codes[token], m.credential, m.machine = false, credential, machine
	case "/agent/v1/heartbeat":
		if presented == "" || presented != m.credential {
			answer(401)
			return
		}
	case "/agent/v1/rotate":
		if presented != m.credential {
			answer(401)
			return
		}
		_, named := body["machineId"]
		if named != modern {
			// Earlier controllers reject the field; later ones require it.
			answer(400)
			return
		}
		if !named {
			m.rotationsWithout++
		} else {
			var machine uuid.UUID
			if json.Unmarshal(body["machineId"], &machine) != nil || machine != m.machine {
				answer(401)
				return
			}
		}
		m.credential = text("credential")
		answer(204)
		return
	default:
		answer(404)
		return
	}
	answer(200)
}

func credentialClient(t *testing.T) (*Client, *credentialController, *httptest.Server) {
	t.Helper()
	j, _ := testVisualJournal(t)
	m := &credentialController{codes: map[string]bool{}}
	server := httptest.NewServer(m)
	t.Cleanup(server.Close)
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin, cfg.MachineID = server.URL, uuid.New()
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := NewClient(j, &Engine{Journal: j})
	c.HTTP = server.Client()
	supported := true
	c.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: api.AgentCapabilities{MachineBoundRotation: &supported}}
	}
	return c, m, server
}

// issue creates a code and, as renewing enrollment does, clears the server's credential.
func (m *credentialController) issue() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	code := strings.Repeat(string(rune('a'+len(m.codes))), 43)
	m.codes[code] = true
	m.credential = ""
	return code
}

func TestRotationNamesTheMachineOnlyToControllersThatReadIt(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "earlier", true: "machine-bound"}[modern], func(t *testing.T) {
			c, m, _ := credentialClient(t)
			m.machineRotation.Store(modern)
			ctx := context.Background()
			if err := c.Enroll(ctx, m.issue()); err != nil {
				t.Fatal(err)
			}
			if err := c.rotate(ctx); err != nil {
				t.Fatal("rotation failed", err)
			}
			cfg, _ := c.Journal.Config()
			if cfg.Pending != "" || cfg.Credential != m.credential {
				t.Fatal("agent and controller disagree after rotation")
			}
			if (m.rotationsWithout == 0) != modern {
				t.Fatal("rotation body didn't follow negotiation")
			}
			if (m.advertised > 0) != modern {
				t.Fatal("machine-bound rotation advertised without negotiation")
			}
		})
	}
}

// The enrollment and the first heartbeat after a restart go out before negotiation, so what the
// agent supports is reported again as soon as the answer arrives.
func TestNegotiatedSupportIsReportedAtOnce(t *testing.T) {
	c, m, _ := credentialClient(t)
	m.machineRotation.Store(true)
	ctx := context.Background()
	if err := c.Enroll(ctx, m.issue()); err != nil {
		t.Fatal(err)
	}
	if m.advertised != 1 {
		t.Fatal("enrollment didn't report negotiated support", m.advertised)
	}
	cfg, _ := c.Journal.Config()
	restarted := NewClient(c.Journal, &Engine{Journal: c.Journal})
	restarted.HTTP, restarted.Observe = c.HTTP, c.Observe
	if err := restarted.heartbeat(ctx, cfg.Credential, &api.AgentStatus{}); err != nil {
		t.Fatal(err)
	}
	if m.advertised != 2 {
		t.Fatal("a restarted agent waited an interval to report negotiated support", m.advertised)
	}
	if err := restarted.heartbeat(ctx, cfg.Credential, &api.AgentStatus{}); err != nil || m.advertised != 3 {
		t.Fatal("a negotiated heartbeat was sent twice", err, m.advertised)
	}
}
