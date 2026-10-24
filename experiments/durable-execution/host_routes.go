// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

type hostRoute struct {
	Project        string `json:"project"`
	BackendProject string `json:"backend_project"`
	Host           string `json:"host"`
	Body           string `json:"body"`
	TCP            bool   `json:"tcp"`
}

type hostRegistry struct {
	Revision string               `json:"revision"`
	Projects map[string]hostRoute `json:"projects"`
}

var hostProjectName = regexp.MustCompile(`^[a-z][a-z0-9]{1,40}$`)
var hostDomainName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*\.test$`)
var errHostInterrupted = errors.New("fixture publisher interrupted")
var errHostNeedsAttention = errors.New("route recovery needs attention")

func (r hostRegistry) validate() error {
	if r.Projects == nil {
		return errors.New("invalid persisted project registry")
	}
	hosts := map[string]bool{}
	tcpClaimed := false
	for id, p := range r.Projects {
		if !hostProjectName.MatchString(id) || p.Project != id || p.BackendProject != id {
			return errors.New("foreign or invalid backend reference")
		}
		if !hostDomainName.MatchString(p.Host) || hosts[p.Host] {
			return errors.New("invalid or conflicting HTTP host claim")
		}
		hosts[p.Host] = true
		if p.TCP && tcpClaimed {
			return errors.New("conflicting TCP listener claim")
		}
		tcpClaimed = tcpClaimed || p.TCP
	}
	return nil
}

func (r hostRegistry) ordered() []string {
	ids := make([]string, 0, len(r.Projects))
	for id := range r.Projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// JSON is a YAML subset accepted by the file provider. Values and references
// come from the typed registry; arbitrary provider fragments are not accepted.
func (r hostRegistry) render(missingBackend string) ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	httpRouters, httpServices := map[string]any{}, map[string]any{}
	tcpRouters, tcpServices := map[string]any{}, map[string]any{}
	for id, p := range r.Projects {
		httpRouters[id] = map[string]any{"entryPoints": []string{"web"}, "rule": "Host(`" + p.Host + "`)", "service": id}
		if id != missingBackend {
			httpServices[id] = map[string]any{"loadBalancer": map[string]any{"servers": []any{map[string]any{"url": "http://" + id + "-app:8080"}}}}
		}
		if p.TCP {
			tcpRouters[id] = map[string]any{"entryPoints": []string{"database"}, "rule": "HostSNI(`*`)", "service": id}
			tcpServices[id] = map[string]any{"loadBalancer": map[string]any{"servers": []any{map[string]any{"address": id + "-db:5432"}}}}
		}
	}
	ids := r.ordered()
	if len(ids) > 0 {
		// The unique probe distinguishes a newly loaded graph from the previous
		// graph still answering during file-provider throttling.
		httpRouters["probe"] = map[string]any{"entryPoints": []string{"web"}, "rule": "Host(`probe-" + r.Revision + ".test`)", "service": ids[len(ids)-1]}
	}
	config := map[string]any{}
	if len(httpRouters) > 0 {
		config["http"] = map[string]any{"routers": httpRouters, "services": httpServices}
	}
	if len(tcpRouters) > 0 {
		config["tcp"] = map[string]any{"routers": tcpRouters, "services": tcpServices}
	}
	return json.Marshal(config)
}

func hostAtomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".candidate-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type hostPublisher struct {
	dir      string
	httpURL  string
	checkTCP func(context.Context, hostRoute) error
	point    func(string) error
}

func (p *hostPublisher) lock(ctx context.Context) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(p.dir, "host.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, err
		}
		if err := p.point("lock_wait"); err != nil {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (p *hostPublisher) approved() (hostRegistry, error) {
	r := hostRegistry{Projects: map[string]hostRoute{}}
	data, err := os.ReadFile(filepath.Join(p.dir, "approved.json"))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(data, &r)
	if err == nil {
		err = r.validate()
	}
	return r, err
}

func hostAwait(ctx context.Context, probe func() error) error {
	var last error
	for {
		if last = probe(); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ctx.Err(), last)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (p *hostPublisher) http(ctx context.Context, host, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.httpURL, nil)
	if err != nil {
		return err
	}
	req.Host = host
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != want {
		return fmt.Errorf("HTTP route %s has unexpected status or content (%d)", host, resp.StatusCode)
	}
	return nil
}

func (p *hostPublisher) verify(ctx context.Context, r hostRegistry) error {
	ids := r.ordered()
	if len(ids) == 0 {
		return errors.New("fixture requires at least one retained project")
	}
	activation, cancel := context.WithTimeout(ctx, 6*time.Second)
	err := hostAwait(activation, func() error { return p.http(activation, "probe-"+r.Revision+".test", r.Projects[ids[len(ids)-1]].Body) })
	cancel()
	if err != nil {
		return err
	}
	if err := p.point("activated"); err != nil {
		return err
	}
	checks, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return hostAwait(checks, func() error {
		for _, id := range ids {
			route := r.Projects[id]
			if err := p.http(checks, route.Host, route.Body); err != nil {
				return err
			}
			if route.TCP {
				if err := p.checkTCP(checks, route); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (p *hostPublisher) restore(ctx context.Context, approved hostRegistry) error {
	if err := p.point("restore"); err != nil {
		return err
	}
	data, err := approved.render("")
	if err != nil {
		return err
	}
	if err := hostAtomicWrite(filepath.Join(p.dir, "routes", "runtime.yaml"), data); err != nil {
		return err
	}
	return p.verify(ctx, approved)
}

func (p *hostPublisher) apply(ctx context.Context, id string, route *hostRoute, fault string) error {
	lock, err := p.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close() // Closing releases flock after verification or rollback.
	previous, err := p.approved()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	var next hostRegistry
	if err := json.Unmarshal(encoded, &next); err != nil {
		return err
	}
	if route == nil {
		delete(next.Projects, id)
	} else {
		next.Projects[id] = *route
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	next.Revision = hex.EncodeToString(nonce[:])
	if err := next.validate(); err != nil {
		return err
	}
	missing := ""
	if fault == "missing_backend" {
		missing = id
	}
	data, err := next.render(missing)
	if err != nil {
		return err
	}
	if fault == "malformed" {
		data = []byte("http: [broken")
	}
	if err := p.point("publish"); err != nil {
		return err
	}
	err = hostAtomicWrite(filepath.Join(p.dir, "routes", "runtime.yaml"), data)
	if err == nil && fault == "interrupted" {
		return errHostInterrupted
	}
	if err == nil {
		err = p.verify(ctx, next)
	}
	if err == nil {
		err = p.point("promote")
	}
	snapshotAttempted := false
	if err == nil {
		approved, encodeErr := json.Marshal(next)
		if encodeErr == nil {
			snapshotAttempted = true
			encodeErr = hostAtomicWrite(filepath.Join(p.dir, "approved.json"), approved)
		}
		err = encodeErr
		if err == nil {
			err = p.point("approved_written")
		}
	}
	if err != nil {
		if snapshotAttempted {
			if recovery := hostAtomicWrite(filepath.Join(p.dir, "approved.json"), encoded); recovery != nil {
				return fmt.Errorf("%w: candidate=%v, registry restore=%v", errHostNeedsAttention, err, recovery)
			}
		}
		if recovery := p.restore(ctx, previous); recovery != nil {
			return fmt.Errorf("%w: candidate=%v, restore=%v", errHostNeedsAttention, err, recovery)
		}
		return fmt.Errorf("candidate rejected; previous routes restored: %w", err)
	}
	return nil
}

func (p *hostPublisher) recover(ctx context.Context) error {
	lock, err := p.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	r, err := p.approved()
	if err != nil {
		return err
	}
	return p.restore(ctx, r)
}
