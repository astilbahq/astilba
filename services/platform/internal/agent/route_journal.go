package agent

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
//	"host"        routeHostRecord: the host's proxy settings, its units, and whether an
//	              attempt to change them may have reached the proxy
//	"set:<unit>"  deployment.RouteSet: one unit's current routes
const (
	routeHostKey   = "host"
	routeSetPrefix = "set:"
	// routeLayout fills the field preceding agents decode as the whole host's routes. A
	// string is not an object, so they refuse the record rather than read no routes and
	// then publish a route file holding only their own unit's.
	routeLayout = "per-unit routes; use a compatible agent"
)

// routeState is a host's routes. While Pending, an attempt to change them may have
// reached the proxy, and recovery puts Current back before anything else changes.
type routeState struct {
	Current routeSnapshot
	Pending bool
}

type routeHostRecord struct {
	Layout  string    `json:"current"`
	Host    routeHost `json:"host"`
	Units   []string  `json:"units"`
	Pending bool      `json:"pending,omitempty"`
}

// routeHost is a snapshot without its sets.
type routeHost struct {
	ManagedEgress     bool   `json:"managedEgress,omitempty"`
	PrivateIngress    bool   `json:"privateIngress,omitempty"`
	Ports             []int  `json:"ports"`
	Revision          string `json:"revision"`
	Email             string `json:"email"`
	ProxyHash         string `json:"proxyHash"`
	QualifiedBackends bool   `json:"qualifiedBackends,omitempty"`
}

// legacyRouteState is the one record preceding agents kept for a host. An attempt's
// previous routes are the ones recovery restores.
type legacyRouteState struct {
	Current routeSnapshot `json:"current"`
	Pending *routeAttempt `json:"pending,omitempty"`
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
	var raw jsontext.Value
	err := j.get(tx, "routing", routeHostKey, &raw)
	if errors.Is(err, os.ErrNotExist) {
		return routeState{Current: routeSnapshot{Sets: map[string]deployment.RouteSet{}, Ports: []int{}}}, nil
	}
	if err != nil {
		return routeState{}, err
	}
	var probe struct {
		Current jsontext.Value `json:"current"`
	}
	if err = json.Unmarshal(raw, &probe); err != nil {
		return routeState{}, err
	}
	if probe.Current.Kind() != '"' {
		// A preceding agent's record. The next save replaces it.
		var legacy legacyRouteState
		if err = json.Unmarshal(raw, &legacy); err != nil {
			return routeState{}, err
		}
		if legacy.Pending != nil {
			return routeState{Current: legacy.Pending.Previous, Pending: true}, nil
		}
		return routeState{Current: legacy.Current}, nil
	}
	var record routeHostRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		return routeState{}, err
	}
	if record.Layout != routeLayout {
		return routeState{}, errors.New("unsupported routing journal layout; use a compatible agent")
	}
	sets := make(map[string]deployment.RouteSet, len(record.Units))
	for _, unit := range record.Units {
		var set deployment.RouteSet
		if err := j.get(tx, "routing", routeSetPrefix+unit, &set); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return routeState{}, errors.New("routing journal is missing a unit's routes")
			}
			return routeState{}, err
		}
		sets[unit] = set
	}
	h := record.Host
	return routeState{Current: routeSnapshot{ManagedEgress: h.ManagedEgress, PrivateIngress: h.PrivateIngress, Sets: sets, Ports: h.Ports, Revision: h.Revision, Email: h.Email, ProxyHash: h.ProxyHash, QualifiedBackends: h.QualifiedBackends}, Pending: record.Pending}, nil
}

// saveRouting stores v and removes the records of units it no longer routes. It writes
// only the units whose stored routes differ, so a publish writes about one unit's routes
// whatever else the host routes.
func (j *Journal) saveRouting(v routeState) error {
	s := v.Current
	record := routeHostRecord{Layout: routeLayout, Host: routeHost{ManagedEgress: s.ManagedEgress, PrivateIngress: s.PrivateIngress, Ports: s.Ports, Revision: s.Revision, Email: s.Email, ProxyHash: s.ProxyHash, QualifiedBackends: s.QualifiedBackends}, Units: []string{}, Pending: v.Pending}
	for unit := range s.Sets {
		record.Units = append(record.Units, unit)
	}
	slices.Sort(record.Units)
	return j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("routing"))
		stored := map[string][]byte{}
		cursor := bucket.Cursor()
		for key, _ := cursor.Seek([]byte(routeSetPrefix)); key != nil && bytes.HasPrefix(key, []byte(routeSetPrefix)); key, _ = cursor.Next() {
			unit := strings.TrimPrefix(string(key), routeSetPrefix)
			stored[unit] = nil
			if _, ok := s.Sets[unit]; ok {
				var raw jsontext.Value
				if err := j.get(tx, "routing", string(key), &raw); err != nil {
					return err
				}
				stored[unit] = raw
			}
		}
		for unit, raw := range stored {
			if raw == nil {
				if err := bucket.Delete([]byte(routeSetPrefix + unit)); err != nil {
					return err
				}
			}
		}
		for unit, set := range s.Sets {
			raw, err := stablejson.Marshal(set)
			if err != nil {
				return err
			}
			if bytes.Equal(raw, stored[unit]) {
				continue
			}
			if err = j.put(tx, "routing", routeSetPrefix+unit, set); err != nil {
				return err
			}
		}
		return j.put(tx, "routing", routeHostKey, record)
	})
}
