package agent

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	bolt "go.etcd.io/bbolt"
)

// releasedValues names, for each kind of command, where its payload carries the values management
// released for that one operation: secret values, registry and Git access, build secrets, and
// backup and upgrade credentials. Every payload's Digest leaves them out, so a command without them
// still matches its plan hash, and a delivery of the same command, with or without its values,
// still matches the record.
var releasedValues = map[api.ExecutionCommandKind][][]string{
	"visual":    {{"secrets"}, {"registryAccess"}},
	"crosshost": {{"secrets"}, {"registryAccess"}},
	"pipeline":  {{"access"}, {"buildSecrets"}, {"registryAccess"}},
	"backup":    {{"installation", "access"}},
	"schedule":  {{"installation", "access"}},
	"upgrade":   {{"secrets"}, {"installation", "access"}},
}

// commandBody is the payload field of a command's kind.
func commandBody(c *api.ExecutionCommand) **jsontext.Value {
	switch c.Kind {
	case "visual":
		return &c.Visual
	case "crosshost":
		return &c.Crosshost
	case "pipeline":
		return &c.Pipeline
	case "backup", "schedule":
		return &c.Backup
	case "upgrade":
		return &c.Upgrade
	}
	return nil
}

// redactCommand removes the values released for a command and reports whether it held any. It
// edits the payload's members rather than decoding it into this agent's types, so members it doesn't
// know, which a later agent's digest may cover, are kept as they are. A payload it can't read is
// dropped whole: nothing can read it either, and none of its values may stay.
func redactCommand(c *api.ExecutionCommand) bool {
	body := commandBody(c)
	if body == nil || *body == nil {
		return false
	}
	redacted, changed := withoutValues(**body, releasedValues[c.Kind])
	if !changed {
		return false
	}
	*body = redacted
	return true
}

// withoutValues deletes each path that holds a value. It returns nil when the payload, or an object
// on a path, is not a JSON object.
func withoutValues(raw jsontext.Value, paths [][]string) (*jsontext.Value, bool) {
	var fields map[string]jsontext.Value
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, true
	}
	changed := false
	for _, path := range paths {
		held, ok := fields[path[0]]
		if !ok {
			continue
		}
		if len(path) == 1 {
			if !emptyValue(held) {
				delete(fields, path[0])
				changed = true
			}
			continue
		}
		inner, innerChanged := withoutValues(held, [][]string{path[1:]})
		if inner == nil {
			return nil, true
		}
		if innerChanged {
			fields[path[0]] = *inner
			changed = true
		}
	}
	if !changed {
		return &raw, false
	}
	encoded, err := stablejson.Marshal(fields)
	if err != nil {
		return nil, true
	}
	out := jsontext.Value(encoded)
	return &out, true
}

// emptyValue reports whether a member holds nothing: null, false, zero, an empty string, or an
// array or object of only such members. An agent writes an unset credential as an object of empty
// strings, and rewriting a record for it would change nothing.
func emptyValue(raw jsontext.Value) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	var empty func(any) bool
	empty = func(v any) bool {
		switch t := v.(type) {
		case nil:
			return true
		case bool:
			return !t
		case float64:
			return t == 0
		case string:
			return t == ""
		case []any:
			for _, e := range t {
				if !empty(e) {
					return false
				}
			}
			return true
		case map[string]any:
			for _, e := range t {
				if !empty(e) {
					return false
				}
			}
			return true
		}
		return false
	}
	return empty(v)
}

// finished reports whether a record's command has ended and management has its outcome. Nothing
// runs it again, and recovery, fencing and redelivery read only its plan, phase and digest.
func (r Record) finished() bool {
	return r.Acknowledged && execution.Terminal(r.Report.State)
}

// upgradeActions are every action a command can ask of an upgrade.
var upgradeActions = []string{"prepare", "cutover", "return", "reconcile"}

// withoutUpgradeValues removes an upgrade's values once it has ended: no action is allowed on it
// again. Until then it keeps them only for an earlier agent restored onto this journal, which runs
// later actions with them; this agent runs each action with its own command's values.
func withoutUpgradeValues(s *upgradeState) bool {
	if slices.ContainsFunc(upgradeActions, s.Status.Allows) || len(s.Command.Secrets) == 0 && s.Command.Installation.Access == (backup.Access{}) {
		return false
	}
	s.Command.Secrets, s.Command.Installation.Access = nil, backup.Access{}
	return true
}

// withoutReleasedValues is what put seals in place of value: a finished record or an ended upgrade
// never reaches the journal with its values, whichever path writes it.
func withoutReleasedValues(bucket string, value any) any {
	switch v := value.(type) {
	case Record:
		if bucket == "operations" && v.finished() {
			redactCommand(&v.Command)
		}
		return v
	case upgradeState:
		if bucket == "upgrades" {
			withoutUpgradeValues(&v)
		}
		return v
	}
	return value
}

// redactFinished removes the values of every finished record and ended upgrade already in the
// journal, as an earlier agent left them. It runs on every open, inside the open's transaction, so a
// crash leaves each record as it was or redacted, never in between, and records an earlier agent
// wrote after a rollback are redacted when this one returns. Unfinished records keep their values
// for the work and redelivery that still need them. Sentinels and fences are never finished and
// carry no payload, so they are left as they are.
func (j *Journal) redactFinished(tx *bolt.Tx) error {
	keys := []string{}
	if err := tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
		keys = append(keys, string(k))
		return nil
	}); err != nil {
		return err
	}
	for _, k := range keys {
		var r Record
		if err := j.get(tx, "operations", k, &r); err != nil {
			return err
		}
		if r.finished() && redactCommand(&r.Command) {
			if err := j.put(tx, "operations", k, r); err != nil {
				return err
			}
		}
	}
	upgrades := map[string]upgradeState{}
	if err := tx.Bucket([]byte("upgrades")).ForEach(func(k, _ []byte) error {
		var s upgradeState
		if err := j.get(tx, "upgrades", string(k), &s); err != nil {
			return err
		}
		if withoutUpgradeValues(&s) {
			upgrades[string(k)] = s
		}
		return nil
	}); err != nil {
		return err
	}
	for k, s := range upgrades {
		if err := j.put(tx, "upgrades", k, s); err != nil {
			return err
		}
	}
	return nil
}
