package agent

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	bolt "go.etcd.io/bbolt"
)

// The routing bucket keeps each unit's routes in a record of its own, so no record grows
// with the number of environments on a host. A host once held every unit's routes in one
// record, three times over while a publish was pending, and one environment's routes
// could then stop every other environment publishing.
//
//	"host"        routeHostRecord: the host's proxy settings, its units and any attempt
//	"set:<unit>"  deployment.RouteSet: one unit's current routes
const (
	routeHostKey   = "host"
	routeSetPrefix = "set:"
	// routeLayout fills the field preceding agents decode as the whole host's routes. A
	// string is not an object, so they refuse the record rather than read no routes and
	// then publish a route file holding only their own unit's.
	routeLayout = "per-unit routes; use a compatible agent"
)

type routeHostRecord struct {
	Layout  string         `json:"current"`
	Host    routeHost      `json:"host"`
	Units   []string       `json:"units"`
	Pending *routeProposal `json:"proposal,omitempty"`
}

// routeHost is a snapshot without its sets.
type routeHost struct {
	ManagedEgress     bool   `json:"managedEgress,omitempty"`
	Ports             []int  `json:"ports"`
	Revision          string `json:"revision"`
	Email             string `json:"email"`
	ProxyHash         string `json:"proxyHash"`
	QualifiedBackends bool   `json:"qualifiedBackends,omitempty"`
}

// routeProposal is an attempt stored as its difference from the current routes, which
// are always its previous routes. A nil set removes that unit's routes.
type routeProposal struct {
	Host routeHost                       `json:"host"`
	Sets map[string]*deployment.RouteSet `json:"sets"`
}

func hostOf(s routeSnapshot) routeHost {
	return routeHost{ManagedEgress: s.ManagedEgress, Ports: s.Ports, Revision: s.Revision, Email: s.Email, ProxyHash: s.ProxyHash, QualifiedBackends: s.QualifiedBackends}
}

func (h routeHost) with(sets map[string]deployment.RouteSet) routeSnapshot {
	return routeSnapshot{ManagedEgress: h.ManagedEgress, Sets: sets, Ports: h.Ports, Revision: h.Revision, Email: h.Email, ProxyHash: h.ProxyHash, QualifiedBackends: h.QualifiedBackends}
}

func (j *Journal) routing() (routeState, error) {
	var v routeState
	err := j.db.View(func(tx *bolt.Tx) (err error) {
		v, err = j.routingIn(tx)
		return err
	})
	return v, err
}

func (j *Journal) routingIn(tx *bolt.Tx) (routeState, error) {
	var probe struct {
		Current jsontext.Value `json:"current"`
	}
	err := j.get(tx, "routing", routeHostKey, &probe)
	if errors.Is(err, os.ErrNotExist) {
		return routeState{Current: routeSnapshot{Sets: map[string]deployment.RouteSet{}, Ports: []int{}}}, nil
	}
	if err != nil {
		return routeState{}, err
	}
	if probe.Current.Kind() != '"' {
		// A preceding agent's record. It is replaced by the next save.
		var legacy routeState
		return legacy, j.get(tx, "routing", routeHostKey, &legacy)
	}
	var record routeHostRecord
	if err = j.get(tx, "routing", routeHostKey, &record); err != nil {
		return routeState{}, err
	}
	if record.Layout != routeLayout {
		return routeState{}, errors.New("unsupported routing journal layout; use a compatible agent")
	}
	current, err := j.routeSetsIn(tx, record.Units)
	if err != nil {
		return routeState{}, err
	}
	state := routeState{Current: record.Host.with(current)}
	if record.Pending != nil {
		// Each snapshot gets maps of its own, because callers change them.
		previous, err := j.routeSetsIn(tx, record.Units)
		if err != nil {
			return routeState{}, err
		}
		candidate, err := j.routeSetsIn(tx, record.Units)
		if err != nil {
			return routeState{}, err
		}
		for unit, set := range record.Pending.Sets {
			if set == nil {
				delete(candidate, unit)
			} else {
				candidate[unit] = *set
			}
		}
		state.Pending = &routeAttempt{Previous: record.Host.with(previous), Candidate: record.Pending.Host.with(candidate)}
	}
	return state, nil
}

func (j *Journal) routeSetsIn(tx *bolt.Tx, units []string) (map[string]deployment.RouteSet, error) {
	sets := make(map[string]deployment.RouteSet, len(units))
	for _, unit := range units {
		var set deployment.RouteSet
		if err := j.get(tx, "routing", routeSetPrefix+unit, &set); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("routing journal is missing a unit's routes")
			}
			return nil, err
		}
		sets[unit] = set
	}
	return sets, nil
}

// saveRouting stores v and removes the records of units it no longer routes. An attempt
// must start from the current routes, as every attempt does, so it is stored as the
// units it changes.
func (j *Journal) saveRouting(v routeState) error {
	record := routeHostRecord{Layout: routeLayout, Host: hostOf(v.Current), Units: []string{}}
	if v.Pending != nil {
		previous, err := stablejson.Marshal(v.Pending.Previous)
		if err != nil {
			return err
		}
		current, err := stablejson.Marshal(v.Current)
		if err != nil {
			return err
		}
		if !bytes.Equal(previous, current) {
			return errors.New("a route attempt must start from the current routes")
		}
		changes, err := changedSets(v.Current.Sets, v.Pending.Candidate.Sets)
		if err != nil {
			return err
		}
		record.Pending = &routeProposal{Host: hostOf(v.Pending.Candidate), Sets: changes}
	}
	for unit := range v.Current.Sets {
		record.Units = append(record.Units, unit)
	}
	slices.Sort(record.Units)
	return j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("routing"))
		stale := [][]byte{}
		cursor := bucket.Cursor()
		for key, _ := cursor.Seek([]byte(routeSetPrefix)); key != nil && bytes.HasPrefix(key, []byte(routeSetPrefix)); key, _ = cursor.Next() {
			if _, ok := v.Current.Sets[strings.TrimPrefix(string(key), routeSetPrefix)]; !ok {
				stale = append(stale, bytes.Clone(key))
			}
		}
		for _, key := range stale {
			if err := bucket.Delete(key); err != nil {
				return err
			}
		}
		for unit, set := range v.Current.Sets {
			if err := j.put(tx, "routing", routeSetPrefix+unit, set); err != nil {
				return err
			}
		}
		return j.put(tx, "routing", routeHostKey, record)
	})
}

// changedSets is each unit whose routes differ between from and to, with its routes in
// to, or nil where to has none.
func changedSets(from, to map[string]deployment.RouteSet) (map[string]*deployment.RouteSet, error) {
	changes := map[string]*deployment.RouteSet{}
	for unit := range from {
		if _, ok := to[unit]; !ok {
			changes[unit] = nil
		}
	}
	for unit, set := range to {
		if before, ok := from[unit]; ok {
			a, err := stablejson.Marshal(before)
			if err != nil {
				return nil, err
			}
			b, err := stablejson.Marshal(set)
			if err != nil {
				return nil, err
			}
			if bytes.Equal(a, b) {
				continue
			}
		}
		changes[unit] = &set
	}
	return changes, nil
}
