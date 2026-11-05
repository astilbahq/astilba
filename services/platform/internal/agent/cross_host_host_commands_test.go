package agent

import (
	"encoding/json/jsontext"
	"sort"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

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
			h.checkpoints[body.ParentID] = deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.NewSHA1(p.EnvironmentID, []byte("native-workspace")), ParentID: body.ParentID, RootHash: body.GroupHash, GroupID: p.Local.GroupID, GroupHash: body.GroupHash, BaseID: id, CheckpointID: id, Digest: deployment.RecoveryDigest(envelope), Bytes: len(envelope), Participants: participants}
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
	h.networkGeneration++
	action := "configure"
	if restrict {
		action = "restrict"
	}
	body := hostnetwork.Command{Action: action, NetworkID: m.NetworkID, MembershipID: m.MembershipID, Manifest: &m}
	if restrict && parent == nil {
		prior, err := (hostnetwork.Host{Dir: h.state}).Snapshot(m.NetworkID)
		if err != nil {
			t.Fatal(err)
		}
		body.Baseline = &prior.Manifest
	}
	raw, _ := stablejson.Marshal(body)
	message := jsontext.Value(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ParentId: parent, ServerId: h.server, Generation: h.networkGeneration, Kind: "network", Action: "inspect", ResourceId: m.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(5 * time.Minute), Network: &message}
	if h.controllerGeneration > 0 {
		c.ControllerGeneration = &h.controllerGeneration
	}
	h.command(t, c)
}
