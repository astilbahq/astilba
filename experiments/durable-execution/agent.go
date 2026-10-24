// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

const ownerLabel = "com.astilba.plat2.run"

type command struct {
	Run         string `json:"run"`
	ID          string `json:"id"`
	Environment string `json:"environment"`
	Generation  uint64 `json:"generation"`
	PlanHash    string `json:"plan_hash"`
	Fence       uint64 `json:"fence"`
	Cancel      bool   `json:"cancel"`
}

func (c command) name() string {
	sum := sha256.Sum256([]byte(c.Run + ":" + c.ID))
	return "plat2-" + hex.EncodeToString(sum[:12])
}

func (c command) samePlan(other command) bool {
	return c.Run == other.Run && c.ID == other.ID && c.Environment == other.Environment &&
		c.Generation == other.Generation && c.PlanHash == other.PlanHash
}

type result struct {
	Command     command `json:"command"`
	State       string  `json:"state"`
	ContainerID string  `json:"container_id"`
	Note        string  `json:"note"`
}

type placement struct {
	Generation uint64 `json:"generation"`
	Active     string `json:"active"`
}

// One mutex serializes this disposable host's mutations. bbolt's file lock also
// prevents two agent processes from opening the same journal.
type agent struct {
	mu     sync.Mutex
	db     *bolt.DB
	docker *client.Client
	image  string
	point  func(string)
}

func openAgent(path, host, image string, point func(string)) (*agent, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	docker, err := client.New(client.WithHost(host))
	if err != nil {
		db.Close()
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"operations", "environments"} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		docker.Close()
		return nil, err
	}
	return &agent{db: db, docker: docker, image: image, point: point}, nil
}

func (a *agent) close() { a.docker.Close(); a.db.Close() }

func (a *agent) save(r result) error {
	return a.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("operations"))
		encoded, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := b.Put([]byte(r.Command.ID), encoded); err != nil {
			return err
		}
		if r.State == "succeeded" || r.State == "cancelled" {
			p := placement{Generation: r.Command.Generation}
			encoded, err = json.Marshal(p)
			if err != nil {
				return err
			}
			return tx.Bucket([]byte("environments")).Put([]byte(r.Command.Environment), encoded)
		}
		return nil
	})
}

func (a *agent) accept(c command) (result, error) {
	if c.Run == "" || c.ID == "" || c.Environment == "" || c.PlanHash == "" || c.Generation == 0 || c.Fence == 0 {
		return result{}, errors.New("incomplete command identity")
	}
	var r result
	err := a.db.Update(func(tx *bolt.Tx) error {
		operations := tx.Bucket([]byte("operations"))
		if data := operations.Get([]byte(c.ID)); data != nil {
			if err := json.Unmarshal(data, &r); err != nil {
				return err
			}
			if !c.samePlan(r.Command) {
				return errors.New("operation ID reused with different plan")
			}
			if c.Fence < r.Command.Fence {
				return errors.New("stale delivery fence")
			}
			r.Command.Fence = c.Fence
			r.Command.Cancel = r.Command.Cancel || c.Cancel
		} else {
			var p placement
			environments := tx.Bucket([]byte("environments"))
			if data := environments.Get([]byte(c.Environment)); data != nil {
				if err := json.Unmarshal(data, &p); err != nil {
					return err
				}
			}
			if c.Generation <= p.Generation || p.Active != "" {
				return errors.New("stale generation or unresolved active operation")
			}
			r = result{Command: c, State: "accepted"}
			encoded, err := json.Marshal(placement{Generation: c.Generation, Active: c.ID})
			if err != nil {
				return err
			}
			if err := environments.Put([]byte(c.Environment), encoded); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return operations.Put([]byte(c.ID), encoded)
	})
	return r, err
}

func (a *agent) inspect(ctx context.Context, c command) (result, error) {
	r := result{Command: c, State: "needs_attention"}
	i, err := a.docker.ContainerInspect(ctx, c.name(), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		r.Note = "Effect intent is durable but container is missing; inspect host history before retrying or replacing this operation."
		return r, nil
	}
	if err != nil {
		return result{}, err
	}
	r.ContainerID = i.Container.ID
	labels := i.Container.Config.Labels
	if labels[ownerLabel] != c.Run || labels["operation"] != c.ID || labels["plan"] != c.PlanHash ||
		labels["generation"] != strconv.FormatUint(c.Generation, 10) {
		r.Note = "Container identity does not match the approved operation; leave it untouched and resolve the name collision."
	} else if !i.Container.State.Running {
		r.Note = "Container exists but is not running; inspect its state and logs before approving another start."
	} else {
		r.State = "succeeded"
		r.Note = "Observed the original owned container running."
	}
	return r, nil
}

func (a *agent) execute(ctx context.Context, c command) (result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.accept(c)
	if err != nil {
		return result{}, err
	}
	if r.State == "succeeded" || r.State == "cancelled" || r.State == "needs_attention" {
		return r, nil
	}
	a.point("accepted")
	if r.State == "accepted" && r.Command.Cancel {
		r.State, r.Note = "cancelled", "Cancelled before any effect intent."
		return r, a.save(r)
	}
	if r.State == "accepted" {
		// Never adopt an existing container just because its name is available in
		// this plan. Only a journaled intent authorizes recovery by inspection.
		_, err := a.docker.ContainerInspect(ctx, c.name(), client.ContainerInspectOptions{})
		if err == nil {
			r.State, r.Note = "needs_attention", "Container name already exists before effect intent; resolve the collision without modifying it."
			return r, a.save(r)
		}
		if !errdefs.IsNotFound(err) {
			return result{}, err
		}
		r.State = "effect_pending"
		if err := a.save(r); err != nil {
			return result{}, err
		}
		a.point("intent")
		created, err := a.docker.ContainerCreate(ctx, client.ContainerCreateOptions{
			Name: c.name(), Image: a.image,
			Config: &container.Config{
				Cmd:    []string{"sh", "-c", "echo effect; exec sleep 600"},
				Labels: map[string]string{ownerLabel: c.Run, "operation": c.ID, "plan": c.PlanHash, "generation": strconv.FormatUint(c.Generation, 10)},
			},
			HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, Resources: container.Resources{Memory: 32 << 20}},
		})
		if err != nil {
			return result{}, err
		}
		a.point("created")
		if _, err := a.docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			return result{}, err
		}
		a.point("effect")
	}
	// Recovery never issues create/start again, even if the resource is missing.
	r, err = a.inspect(ctx, r.Command)
	if err != nil {
		return result{}, err
	}
	if r.Command.Cancel && r.State == "succeeded" {
		r.State, r.Note = "cancelled", "Cancellation reconciled after the effect; original container retained."
	}
	if err := a.save(r); err != nil {
		return result{}, err
	}
	a.point("recorded")
	return r, nil
}

func (a *agent) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var c command
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The accepted operation outlives a disconnected worker. This is a local
	// Unix-socket fixture; production authentication/HTTPS is a separate spike.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := a.execute(ctx, c)
	if err != nil {
		http.Error(w, fmt.Sprint(err), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(r)
}
