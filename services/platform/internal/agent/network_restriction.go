package agent

import (
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func standaloneRestriction(c api.ExecutionCommand) bool {
	if c.ParentId != nil || c.EnvironmentId != uuid.Nil || c.Action != "inspect" || c.Image != "" || c.Release != nil || c.RecoveryCheckpoint != nil || c.Visual != nil || c.Pipeline != nil || c.Crosshost != nil || c.Backup != nil || c.Upgrade != nil || !networkRestriction(c) {
		return false
	}
	var command hostnetwork.Command
	return hostnetwork.Decode(*c.Network, &command) == nil && command.Baseline != nil && command.Manifest.ServerID == c.ServerId && command.NetworkID == c.ResourceId && hostnetwork.Hash(command) == c.PlanHash
}

func (j *Journal) networkRestrictionScope(c api.ExecutionCommand, command hostnetwork.Command) (hostnetwork.Manifest, uuid.UUID, error) {
	if c.ParentId == nil {
		if command.Baseline == nil {
			return hostnetwork.Manifest{}, uuid.Nil, errors.New("standalone restriction has no frozen baseline")
		}
		return *command.Baseline, uuid.Nil, nil
	}
	var baseline hostnetwork.Manifest
	var unit uuid.UUID
	err := j.db.View(func(tx *bolt.Tx) error {
		active, err := j.crossHostIn(tx)
		if err != nil {
			return err
		}
		if active.ParentID == *c.ParentId {
			baseline, unit = active.Unit.Plan.Local.Network, active.Unit.Plan.UnitID
			return nil
		}
		var recovered recoveredAuthority
		if j.get(tx, "recovery-authority", "current", &recovered) == nil && recovered.Handover.Authority.ParentID == *c.ParentId {
			baseline, unit = recovered.Handover.Network, recovered.Handover.UnitID
			return nil
		}
		d, err := commandCheckpoint(c)
		if err != nil {
			return err
		}
		return tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
			var r Record
			if err := j.get(tx, "operations", string(k), &r); err != nil {
				return err
			}
			if r.Command.ParentId == nil || *r.Command.ParentId != *c.ParentId || r.Command.Kind != "crosshost" {
				return nil
			}
			body, err := crossHostCommand(r.Command)
			checkpoint, e := commandCheckpoint(r.Command)
			if err != nil || e != nil || !deployment.SameRecoveryRoot(d, checkpoint) || body.Unit.Plan.Local == nil {
				return errors.New("retained network root differs from the parent")
			}
			if unit != uuid.Nil && (unit != body.Unit.Plan.UnitID || hostnetwork.Hash(baseline) != hostnetwork.Hash(body.Unit.Plan.Local.Network)) {
				return errors.New("retained parent network authority diverged")
			}
			baseline, unit = body.Unit.Plan.Local.Network, body.Unit.Plan.UnitID
			return nil
		})
	})
	if err == nil && unit == uuid.Nil {
		err = errors.New("no retained parent network authority")
	}
	return baseline, unit, err
}
