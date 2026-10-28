package execution

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
)

func recoveryChildFrame(parent api.ExecutionCommand, root coordinationCommand, phase string, header api.ExecutionCommand) (api.ExecutionCommand, error) {
	var payload api.ExecutionCommand
	var unit deployment.DistributedUnit
	for _, candidate := range root.Plan.Distributed.Units {
		if candidate.Plan.ServerID == header.ServerId {
			unit = candidate
		}
	}
	if unit.Plan.Hash == "" || header.ParentId == nil || *header.ParentId != parent.Id || header.EnvironmentId != parent.EnvironmentId || header.Id != phaseKey(parent.Id, phase, unit.Plan.UnitID) || header.Generation < 1 || header.Deadline.IsZero() {
		return header, errors.New("recovery child identity differs from its frozen phase")
	}
	if root.Maintenance != nil {
		found := false
		for _, candidate := range root.Maintenance.Units {
			if candidate.Unit.Plan.ServerID == header.ServerId {
				payload = maintenanceChildPayload(parent, phase, root.Maintenance, candidate)
				found = true
			}
		}
		if !found {
			return header, errors.New("maintenance recovery participant is absent")
		}
	} else if header.Kind == "network" {
		if phase != "links" && phase != "restrict" && phase != "abort-restrict" {
			return header, errors.New("unexpected recovery network phase")
		}
		var command hostnetwork.Command
		if header.Network == nil || hostnetwork.Decode(*header.Network, &command) != nil || command.Manifest == nil || command.Validate() != nil || hostnetwork.Hash(command) != header.PlanHash || command.Manifest.ServerID != unit.Plan.ServerID || command.NetworkID != root.Plan.Distributed.NetworkID {
			return header, errors.New("invalid frozen recovery network payload")
		}
		if phase == "links" && (command.Action != "configure" || hostnetwork.Hash(*command.Manifest) != hostnetwork.Hash(unit.Plan.Local.Network)) || phase != "links" && command.Action != "restrict" {
			return header, errors.New("recovery network authority differs from its phase")
		}
		return header, nil
	} else {
		if !slices.Contains([]string{"checkpoint", "reserve", "prepare", "hook", "probe", "publish", "retire", "complete", "abort"}, phase) {
			return header, errors.New("unsupported recovered deployment phase")
		}
		body := deployment.CrossHostCommand{ParentID: parent.Id, GroupHash: root.Plan.Hash, Phase: phase, Unit: unit}
		raw, _ := json.Marshal(body)
		payload = api.ExecutionCommand{Kind: "crosshost", Action: "inspect", ServerId: unit.Plan.ServerID, EnvironmentId: unit.Plan.EnvironmentID, ResourceId: unit.Plan.UnitID, PlanHash: body.Digest(), Crosshost: rawMessage(raw)}
	}
	if payload.Kind != header.Kind || payload.Action != header.Action || payload.ServerId != header.ServerId || payload.EnvironmentId != header.EnvironmentId || payload.ResourceId != header.ResourceId || payload.PlanHash != header.PlanHash {
		return header, errors.New("recovery header differs from its frozen payload")
	}
	header.Crosshost, header.Upgrade, header.Backup = payload.Crosshost, payload.Upgrade, payload.Backup
	return header, nil
}
