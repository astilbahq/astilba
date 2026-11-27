package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deadline"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

type hostRequest struct {
	Command     *api.ExecutionCommand `json:"command,omitempty"`
	Acknowledge *api.AgentReport      `json:"acknowledge,omitempty"`
	Membership  *uuid.UUID            `json:"membership,omitempty"`
	Inventory   bool                  `json:"inventory,omitempty"`
	Policy      *uuid.UUID            `json:"policy,omitempty"`
	Upgrade     *uuid.UUID            `json:"upgrade,omitempty"`
	Transfer    bool                  `json:"transfer,omitempty"`
	Barrier     bool                  `json:"barrier,omitempty"`
	Retry       *uuid.UUID            `json:"retry,omitempty"`
	Reject      *uuid.UUID            `json:"reject,omitempty"`
	CrashAt     string                `json:"crashAt,omitempty"`
	CancelAt    string                `json:"cancelAt,omitempty"`
}
type hostReply struct {
	Report    *api.AgentReport        `json:"report,omitempty"`
	Identity  *hostnetwork.Identity   `json:"identity,omitempty"`
	Inventory *api.CrossHostInventory `json:"inventory,omitempty"`
	Status    *backup.Status          `json:"status,omitempty"`
	Upgrade   *upgrade.Status         `json:"upgrade,omitempty"`
	Barrier   *bool                   `json:"barrier,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

func nativeRecoveryEnvelope(parent uuid.UUID) []byte {
	return []byte("native-fixture-opaque-metadata:" + parent.String())
}

// interrupted stops the agent at a point in a phase, as a SIGTERM does ("start" before the
// phase begins). The phase must record no outcome, so that a restarted agent reruns it.
func (h *nativePlacementHost) call(t *testing.T, request hostRequest) hostReply {
	t.Helper()
	if c := request.Command; c != nil && c.ParentId != nil {
		if d, ok := h.checkpoints[*c.ParentId]; ok {
			raw, _ := stablejson.Marshal(d)
			message := jsontext.Value(raw)
			c.RecoveryCheckpoint = &message
			if c.EnvironmentId == uuid.Nil {
				c.EnvironmentId = h.checkpointEnvironments[*c.ParentId]
			}
			body, err := deadline.CrossHost(*c)
			if err == nil && body.Phase == "reserve" && body.Unit.Plan.Local.Primary && !h.checkpointReady[*c.ParentId] {
				checkpoint := *c
				body.Phase = "checkpoint"
				payload, _ := stablejson.Marshal(body)
				msg := jsontext.Value(payload)
				checkpoint.Id, checkpoint.Generation, checkpoint.Crosshost, checkpoint.PlanHash = uuid.New(), c.Generation-1, &msg, body.Digest()
				h.command(t, checkpoint)
				h.checkpointReady[*c.ParentId] = true
			}
		}
	}
	if err := stablejson.Encode(h.input, request); err != nil {
		t.Fatal(err)
	}
	var reply hostReply
	if err := json.UnmarshalDecode(h.output, &reply); err != nil {
		t.Fatalf("host process response: %v", err)
	}
	if reply.Error != "" {
		t.Fatal(reply.Error)
	}
	return reply
}
func (h *nativePlacementHost) command(t *testing.T, c api.ExecutionCommand) api.AgentReport {
	t.Helper()
	reply := h.call(t, hostRequest{Command: &c})
	if reply.Report == nil {
		t.Fatal("missing host receipt")
	}
	if reply.Report.State != "succeeded" {
		for _, args := range [][]string{{"wg", "show"}, {"ip", "route"}, {"nft", "list", "ruleset"}} {
			out, _ := exec.Command("nsenter", append([]string{"--net=/run/netns/" + h.namespace}, args...)...).CombinedOutput()
			t.Logf("host fixture %s: %s", args[0], out)
		}
		t.Fatalf("host phase %s: %s", c.Kind, reply.Report.Note)
	}
	h.call(t, hostRequest{Acknowledge: reply.Report})
	return *reply.Report
}

func (h *nativePlacementHost) interrupted(t *testing.T, c api.ExecutionCommand, at string) {
	t.Helper()
	reply := h.call(t, hostRequest{Command: &c, CancelAt: at})
	if reply.Report == nil || reply.Report.State != "running" {
		t.Fatalf("stopping at %s recorded an outcome: %+v", at, reply.Report)
	}
}

func (h *nativePlacementHost) visual(t *testing.T, action string, p deployment.Plan, secrets map[string]string) api.AgentReport {
	t.Helper()
	h.generation++
	body := deployment.Payload{Action: action, Plan: p, Secrets: secrets}
	raw, _ := stablejson.Marshal(body)
	message := jsontext.Value(raw)
	return h.command(t, api.ExecutionCommand{Id: uuid.New(), ServerId: h.server, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Generation: h.generation, Kind: "visual", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(5 * time.Minute), Visual: &message})
}

func (h *nativePlacementHost) crossHost(t *testing.T, parent uuid.UUID, hash, phase string, unit deployment.DistributedUnit, secrets map[string]string) api.AgentReport {
	t.Helper()
	return h.command(t, h.crossHostCommand(parent, hash, phase, unit, secrets))
}

func (h *nativePlacementHost) crossHostCommand(parent uuid.UUID, hash, phase string, unit deployment.DistributedUnit, secrets map[string]string) api.ExecutionCommand {
	body := deployment.CrossHostCommand{ParentID: parent, GroupHash: hash, Phase: phase, Unit: unit, Secrets: secrets}
	return h.coordinatedCommand(body)
}

func (h *nativePlacementHost) coordinatedCommand(body deployment.CrossHostCommand) api.ExecutionCommand {
	if h.checkpoints == nil {
		h.checkpoints = map[uuid.UUID]deployment.RecoveryDescriptor{}
		h.checkpointReady = map[uuid.UUID]bool{}
		h.checkpointEnvironments = map[uuid.UUID]uuid.UUID{}
	}
	if body.Phase != "resolve" && body.Phase != "observe" {
		if _, ok := h.checkpoints[body.ParentID]; !ok {
			p := body.Unit.Plan
			participants := []uuid.UUID{p.ServerID}
			for _, peer := range p.Local.Network.Peers {
				participants = append(participants, peer.ServerID)
			}
			sort.Slice(participants, func(i, j int) bool { return participants[i].String() < participants[j].String() })
			id := uuid.NewSHA1(body.ParentID, []byte("native-recovery-checkpoint"))
			envelope := nativeRecoveryEnvelope(body.ParentID)
			h.checkpoints[body.ParentID] = deployment.RecoveryDescriptor{Version: deployment.RecoveryVersion, WorkspaceID: uuid.NewSHA1(p.EnvironmentID, []byte("native-workspace")), ParentID: body.ParentID, RootHash: body.GroupHash, GroupID: p.Local.GroupID, GroupHash: body.GroupHash, BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(envelope), Bytes: len(envelope), Participants: participants}
			h.checkpointEnvironments[body.ParentID] = p.EnvironmentID
			if p.Local.Primary && body.Phase == "reserve" {
				h.generation++ // The primary metadata ACK precedes reserve.
			}
		}
	}
	h.generation++
	raw, _ := stablejson.Marshal(body)
	message := jsontext.Value(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: &body.ParentID, ServerId: h.server, EnvironmentId: body.Unit.Plan.EnvironmentID, ResourceId: body.Unit.Plan.UnitID, Generation: h.generation, Kind: "crosshost", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(5 * time.Minute), Crosshost: &message}
	if d, ok := h.checkpoints[body.ParentID]; ok {
		raw, _ := stablejson.Marshal(d)
		checkpoint := jsontext.Value(raw)
		c.RecoveryCheckpoint = &checkpoint
	}
	if h.controllerGeneration > 0 {
		c.ControllerGeneration = &h.controllerGeneration
	}
	return c
}

func (h *nativePlacementHost) backupCommand(body backup.Command, parent *uuid.UUID) api.ExecutionCommand {
	h.generation++
	raw, _ := stablejson.Marshal(body)
	message := jsontext.Value(raw)
	p := body.Installation.Policy
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: parent, ServerId: h.server, EnvironmentId: p.EnvironmentID, ResourceId: p.ID, Generation: h.generation, Kind: "backup", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(5 * time.Minute), Backup: &message}
	if parent != nil {
		if d, ok := h.checkpoints[*parent]; ok {
			raw, _ := stablejson.Marshal(d)
			checkpoint := jsontext.Value(raw)
			c.RecoveryCheckpoint = &checkpoint
		}
	}
	return c
}

func (h *nativePlacementHost) network(t *testing.T, parent *uuid.UUID, m hostnetwork.Manifest, restrict bool) {
	t.Helper()
	var baseline *hostnetwork.Manifest
	if restrict && parent == nil {
		prior, err := (hostnetwork.Host{Dir: h.state}).Snapshot(m.NetworkID)
		if err != nil {
			t.Fatal(err)
		}
		baseline = &prior.Manifest
	}
	h.command(t, h.networkCommand(parent, m, restrict, baseline))
}

func (h *nativePlacementHost) networkCommand(parent *uuid.UUID, m hostnetwork.Manifest, restrict bool, baseline *hostnetwork.Manifest) api.ExecutionCommand {
	h.networkGeneration++
	action := "configure"
	if restrict {
		action = "restrict"
	}
	body := hostnetwork.Command{Action: action, NetworkID: m.NetworkID, MembershipID: m.MembershipID, Manifest: &m, Baseline: baseline}
	raw, _ := stablejson.Marshal(body)
	message := jsontext.Value(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: parent, ServerId: h.server, Generation: h.networkGeneration, Kind: "network", Action: "inspect", ResourceId: m.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(5 * time.Minute), Network: &message}
	if h.controllerGeneration > 0 {
		c.ControllerGeneration = &h.controllerGeneration
	}
	return c
}

// A backup is what an owner wants most when an application is down. It stops what is running,
// takes the backup and restarts what it stopped, and the stopped writer holds nothing up.
func nativeBackupOfStoppedWriter(t *testing.T, hosts []*nativePlacementHost, units []deployment.DistributedUnit, completed []deployment.Observation, app uuid.UUID, secrets map[string]string, installation backup.Installation) {
	t.Helper()
	ctx := context.Background()
	primary, remote := hosts[0], hosts[1]
	writer := deployment.UnitName(units[1].Plan.InstanceIDs(app)[0])
	serving := deployment.UnitName(units[0].Plan.InstanceIDs(app)[0])
	if _, err := remote.docker.ContainerStop(ctx, writer, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	// A writer stopped before a backup has normally been out of its pool since then. One
	// stopped within a probe interval of the reservation still reads as routed there, and
	// its completion would wait out the bound for it.
	for _, endpoint := range units[0].Plan.Local.Backends[app.String()] {
		if endpoint.ServerID == remote.server {
			nativeWaitServerOut(t, primary, "http://"+net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.Port)))
		}
	}
	observe := func() {
		t.Helper()
		parent := uuid.New()
		for index, host := range hosts {
			var receipt deployment.CrossHostReceipt
			r := host.crossHost(t, parent, strings.Repeat("3", 64), "observe", units[index], secretsForPlan(units[index].Plan, secrets))
			if err := json.Unmarshal(*r.Crosshost, &receipt); err != nil {
				t.Fatal(err)
			}
			completed[index] = receipt.Observation
		}
	}
	observe()
	parent, run := uuid.New(), uuid.New()
	effect := backup.Command{Action: "backup", Installation: installation, RunID: run}
	intents := make([]deployment.Maintenance, len(hosts))
	for index := range hosts {
		intents[index] = deployment.Maintenance{Kind: "backup", ResourceID: installation.Policy.ID, RunID: run, EffectHash: effect.Digest(), ExpectedState: completed[index].StateHash}
	}
	command := func(index int, phase string) api.ExecutionCommand {
		body := deployment.CrossHostCommand{ParentID: parent, GroupHash: strings.Repeat("4", 64), Phase: phase, Unit: units[index], Secrets: secretsForPlan(units[index].Plan, secrets), Maintenance: &intents[index]}
		if phase == "effect" {
			body.EffectAction = "backup"
		}
		return hosts[index].coordinatedCommand(body)
	}
	for _, phase := range []string{"reserve", "pause"} {
		for index, host := range hosts {
			host.command(t, command(index, phase))
		}
	}
	primary.command(t, command(0, "effect"))
	primary.command(t, primary.backupCommand(effect, &parent))
	status := primary.call(t, hostRequest{Policy: &installation.Policy.ID}).Status
	if status == nil || status.Runs[len(status.Runs)-1].ID != run || status.Runs[len(status.Runs)-1].State != "succeeded" {
		t.Fatalf("the backup did not succeed: %+v", status)
	}
	for _, phase := range []string{"resume-source", "complete"} {
		for index, host := range hosts {
			host.command(t, command(index, phase))
		}
	}
	for _, host := range hosts {
		if blocked := host.call(t, hostRequest{Barrier: true}).Barrier; blocked == nil || *blocked {
			t.Fatal("the backup left its reservation held")
		}
	}
	if v, err := primary.docker.ContainerInspect(ctx, serving, client.ContainerInspectOptions{}); err != nil || !v.Container.State.Running {
		t.Fatal("the backup did not restart the writer it stopped", err)
	}
	v, err := remote.docker.ContainerInspect(ctx, writer, client.ContainerInspectOptions{})
	if err != nil || v.Container.State.Running {
		t.Fatal("the backup started a writer that was stopped before it began", err)
	}
	// Starting it again by hand must not read as drift, which would refuse a deployment.
	if _, err = remote.docker.ContainerStart(ctx, writer, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	observe()
	for _, resource := range completed[1].Resources {
		if resource.Drift {
			t.Fatalf("a restarted writer reads as drift: %+v", resource)
		}
	}
	nativeWaitPlacementRecovery(t)
}

// nativeWaitServerOut waits until host's proxy reports server up in no pool, as it does
// within a probe interval of the server failing.
func nativeWaitServerOut(t *testing.T, host *nativePlacementHost, server string) {
	t.Helper()
	for started := time.Now(); ; time.Sleep(200 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		raw, err := proxyRawdata(ctx, host.docker, proxyName)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		var runtime struct {
			Services map[string]struct {
				ServerStatus map[string]string `json:"serverStatus"`
			} `json:"services"`
		}
		if err = json.Unmarshal(raw, &runtime); err != nil {
			t.Fatal(err)
		}
		up := false
		for _, service := range runtime.Services {
			up = up || service.ServerStatus[server] == "UP"
		}
		if !up {
			return
		}
		if time.Since(started) > 3*deployment.PoolHealthInterval {
			t.Fatalf("the proxy still routes %s", server)
		}
	}
}

// Restoring a database its owner stopped starts the restored one. Everything depending on it
// is failing when its host reserves, so nothing waits for those instances afterwards.
func nativeStopDatabaseBeforeRestore(t *testing.T, hosts []*nativePlacementHost, group deployment.Plan, completed []deployment.Observation, database uuid.UUID, secrets map[string]string) {
	t.Helper()
	if _, err := hosts[0].docker.ContainerStop(context.Background(), deployment.UnitName(database), client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	for index, host := range hosts {
		unit := group.Distributed.Units[index]
		var receipt deployment.CrossHostReceipt
		r := host.crossHost(t, uuid.New(), group.Hash, "observe", unit, secretsForPlan(unit.Plan, secrets))
		if err := json.Unmarshal(*r.Crosshost, &receipt); err != nil {
			t.Fatal(err)
		}
		completed[index] = receipt.Observation
	}
}

func secretsForPlan(p deployment.Plan, secrets map[string]string) map[string]string {
	out := map[string]string{}
	for _, id := range p.SecretReferences() {
		out[id.String()] = secrets[id.String()]
	}
	return out
}
