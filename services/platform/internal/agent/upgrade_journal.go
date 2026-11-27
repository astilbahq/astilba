package agent

import (
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type upgradeState struct {
	OperationID uuid.UUID

	Command upgrade.Command
	Status  upgrade.Status
	Writers []string
}

func (j *Journal) upgrade(id uuid.UUID) (s upgradeState, err error) {
	err = j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "upgrades", id.String(), &s) })
	return
}
func (j *Journal) saveUpgrade(s upgradeState) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("upgrades"))
		if bucket.Get([]byte(s.Command.Plan.ID.String())) == nil && bucket.Stats().KeyN >= 100 {
			return errors.New("upgrade history limit reached")
		}
		return j.put(tx, "upgrades", s.Command.Plan.ID.String(), s)
	})
}

// takeValues gives the upgrade the values a command for its plan was delivered with, so each action
// runs with its own. The journal keeps an upgrade's values only while an action can still run on
// it, and only for an earlier agent restored onto it, which reads them from here. A command
// delivered without them, such as one cancelled before this host took it, needs none and leaves
// those kept as they are.
func (s *upgradeState) takeValues(cmd upgrade.Command) {
	if len(cmd.Secrets) > 0 {
		s.Command.Secrets = cmd.Secrets
	}
	if cmd.Installation.Access != (backup.Access{}) {
		s.Command.Installation.Access = cmd.Installation.Access
	}
}

// UpgradeViews omits the values an active upgrade keeps.
func (j *Journal) UpgradeViews() ([]upgrade.View, error) {
	states, err := j.upgrades()
	if err != nil {
		return nil, err
	}
	out := []upgrade.View{}
	for _, s := range states {
		out = append(out, upgrade.View{Plan: s.Command.Plan, Status: s.Status})
	}
	return out, nil
}
func (j *Journal) upgrades() (out []upgradeState, err error) {
	err = j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("upgrades")).ForEach(func(k, v []byte) error {
			var s upgradeState
			if err := j.get(tx, "upgrades", string(k), &s); err != nil {
				return err
			}
			out = append(out, s)
			return nil
		})
	})
	return
}
