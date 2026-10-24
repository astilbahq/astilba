package agent

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"math"
	"os"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	bolt "go.etcd.io/bbolt"
)

func (j *Journal) Inventory() (api.ControllerInventory, error) {
	out := api.ControllerInventory{Units: map[string]string{}, Schedules: map[string]int64{}}
	err := j.db.View(func(tx *bolt.Tx) error {
		if e := tx.Bucket([]byte("units")).ForEach(func(k, v []byte) error {
			var unit unitState
			if e := j.get(tx, "units", string(k), &unit); e != nil {
				return e
			}
			if unit.Plan.Hash != "" {
				out.Units[string(k)] = unit.Plan.Hash
			}
			return nil
		}); e != nil {
			return e
		}
		return tx.Bucket([]byte("schedules")).ForEach(func(k, v []byte) error {
			var state scheduleState
			if e := j.get(tx, "schedules", string(k), &state); e != nil {
				return e
			}
			out.Schedules[string(k)] = state.Installation.Policy.Generation
			return nil
		})
	})
	if len(out.Units) > 100 || len(out.Schedules) > 100 {
		return out, errors.New("transfer inventory limit exceeded")
	}
	return out, err
}

// Transfer requires the exclusive journal lock, held only while the agent daemon
// is stopped. The local owner replaces the full controller trust in one commit.
func (j *Journal) Transfer(origin string, key []byte, generation int64, token string) error {
	if ValidateOrigin(origin) != nil || len(key) != ed25519.PublicKeySize || generation < 1 || len(strings.TrimSpace(token)) != 43 {
		return errors.New("valid target origin, release key and generation are required")
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		var cfg Config
		if e := j.get(tx, "identity", "config", &cfg); e != nil {
			return e
		}
		if !cfg.Enrolled && cfg.Origin == origin && cfg.ControllerGeneration == generation && bytes.Equal(cfg.PublicKey, key) {
			cfg.EnrollmentToken = strings.TrimSpace(token)
			return j.put(tx, "identity", "config", cfg)
		}
		if cfg.ControllerGeneration == math.MaxInt64 || generation <= cfg.ControllerGeneration {
			return errors.New("target generation must exceed the installed controller generation")
		}
		completed := []Record{}
		if e := tx.Bucket([]byte("operations")).ForEach(func(k, v []byte) error {
			var r Record
			if e := j.get(tx, "operations", string(k), &r); e != nil {
				return e
			}
			if !execution.Terminal(r.Report.State) {
				return errors.New("resolve unfinished operations before transfer")
			}
			if !r.Acknowledged {
				completed = append(completed, r)
			}
			return nil
		}); e != nil {
			return e
		}
		if e := tx.Bucket([]byte("upgrades")).ForEach(func(k, v []byte) error {
			var state upgradeState
			if e := j.get(tx, "upgrades", string(k), &state); e != nil {
				return e
			}
			if state.Status.Active() {
				return errors.New("resolve active database upgrades before transfer")
			}
			return nil
		}); e != nil {
			return e
		}
		if e := tx.Bucket([]byte("schedules")).ForEach(func(k, v []byte) error {
			var state scheduleState
			if e := j.get(tx, "schedules", string(k), &state); e != nil {
				return e
			}
			for _, run := range state.Runs {
				if run.State == "running" || run.State == "needs_attention" {
					return errors.New("resolve uncertain schedule runs before transfer")
				}
			}
			return nil
		}); e != nil {
			return e
		}
		for _, r := range completed {
			r.Acknowledged = true
			if e := j.put(tx, "operations", r.Command.Id.String(), r); e != nil {
				return e
			}
		}
		// Execution counters belong to the old authority. A restored management
		// database may have older counters; the controller generation fences all
		// old commands while units and schedule generations remain unchanged.
		if e := tx.DeleteBucket([]byte("generations")); e != nil {
			return e
		}
		if _, e := tx.CreateBucket([]byte("generations")); e != nil {
			return e
		}
		cfg.Origin = origin
		cfg.PublicKey = append([]byte(nil), key...)
		cfg.Credential = ""
		cfg.Pending = ""
		cfg.Enrolled = false
		cfg.EnrollmentToken = strings.TrimSpace(token)
		cfg.ControllerGeneration = generation
		return j.put(tx, "identity", "config", cfg)
	})
}

func commandGeneration(c api.ExecutionCommand) int64 {
	if c.ControllerGeneration == nil {
		return 0
	}
	return *c.ControllerGeneration
}
func (j *Journal) checkController(tx *bolt.Tx, c api.ExecutionCommand) error {
	var cfg Config
	e := j.get(tx, "identity", "config", &cfg)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if cfg.ControllerGeneration != commandGeneration(c) {
		return errors.New("command belongs to another controller generation")
	}
	return nil
}
