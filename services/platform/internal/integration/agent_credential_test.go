package integration

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/google/uuid"
)

// A second holder of the current credential can't finish an owner's rotation before the agent
// does, because it can't name the machine that enrolled.
func TestRotationNeedsTheEnrolledMachine(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, token := enrollment(t, f, b, ws, "deployment")
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	defer tlsServer.Close()
	client := tlsServer.Client()
	agentRequest(t, client, tlsServer.URL, "/agent/v1/enroll", "", in, 200, nil)
	route := "/api/v1/workspaces/" + ws.String() + "/servers/" + v.Server.Id.String()
	// An owner can no longer ask a bearer agent to rotate: it is told to update instead, and
	// binds a key. A request made before management was upgraded can still be finished.
	b.call("POST", route+"/rotate", nil, 409, nil)
	if _, e := f.st.Pool.Exec(context.Background(), "UPDATE servers SET rotation_requested=true WHERE id=$1", v.Server.Id); e != nil {
		t.Fatal(e)
	}

	stolen, _ := randtoken.New()
	agentRequest(t, client, tlsServer.URL, "/agent/v1/rotate", token, api.AgentRotation{Credential: stolen, MachineId: uuid.New()}, 401, nil)
	agentRequest(t, client, tlsServer.URL, "/agent/v1/rotate", token, map[string]string{"credential": stolen}, 400, nil)
	heartbeat := api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}
	var status api.AgentStatus
	agentRequest(t, client, tlsServer.URL, "/agent/v1/heartbeat", token, heartbeat, 200, &status)
	if !status.RotationRequested {
		t.Fatal("a refused exchange settled the owner's rotation")
	}
	agentRequest(t, client, tlsServer.URL, "/agent/v1/heartbeat", v.Server.Id.String()+"."+stolen, heartbeat, 401, nil)

	next, _ := randtoken.New()
	agentRequest(t, client, tlsServer.URL, "/agent/v1/rotate", token, api.AgentRotation{Credential: next, MachineId: in.MachineId}, 204, nil)
	agentRequest(t, client, tlsServer.URL, "/agent/v1/heartbeat", v.Server.Id.String()+"."+next, heartbeat, 200, &status)
	if status.RotationRequested {
		t.Fatal("rotation stayed requested after the agent finished it")
	}
}

// An agent that can't name its machine is neither offered a rotation nor told about one: it would
// keep presenting a candidate management refuses. A real agent enrolls before negotiation, so
// management learns that it can only from the heartbeat that follows.
func TestRotationWaitsForAnAgentThatNamesItsMachine(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	v, in, token := enrollment(t, f, b, ws, "deployment")
	supported := in.Capabilities.MachineBoundRotation
	in.Capabilities.MachineBoundRotation = nil
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	defer tlsServer.Close()
	client := tlsServer.Client()
	agentRequest(t, client, tlsServer.URL, "/agent/v1/enroll", "", in, 200, nil)
	route := "/api/v1/workspaces/" + ws.String() + "/servers/" + v.Server.Id.String()
	b.call("POST", route+"/rotate", nil, 409, nil)
	var requested bool
	if e := f.st.Pool.QueryRow(context.Background(), "SELECT rotation_requested FROM servers WHERE id=$1", v.Server.Id).Scan(&requested); e != nil || requested {
		t.Fatal("a refused rotation request was kept", e)
	}

	// A request made before the agent was replaced by an older one.
	if _, e := f.st.Pool.Exec(context.Background(), "UPDATE servers SET rotation_requested=true WHERE id=$1", v.Server.Id); e != nil {
		t.Fatal(e)
	}
	var status api.AgentStatus
	heartbeat := func() {
		t.Helper()
		agentRequest(t, client, tlsServer.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, &status)
	}
	heartbeat()
	if status.RotationRequested {
		t.Fatal("an agent that can't finish a rotation was asked to start one")
	}
	if _, e := f.st.Pool.Exec(context.Background(), "UPDATE servers SET rotation_requested=false WHERE id=$1", v.Server.Id); e != nil {
		t.Fatal(e)
	}

	in.Capabilities.MachineBoundRotation = supported
	heartbeat()
	b.call("POST", route+"/rotate", nil, 409, nil)
	if _, e := f.st.Pool.Exec(context.Background(), "UPDATE servers SET rotation_requested=true WHERE id=$1", v.Server.Id); e != nil {
		t.Fatal(e)
	}
	heartbeat()
	if !status.RotationRequested {
		t.Fatal("an agent that names its machine wasn't told about the rotation it was asked for")
	}
}
