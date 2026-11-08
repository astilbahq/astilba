package agent

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

func renderSnapshot(s routeSnapshot) ([]byte, error) {
	render := deployment.BareRouteConfig
	if s.QualifiedBackends {
		render = deployment.RouteConfig
	}
	raw, err := render(s.Sets)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if s.Revision != "" {
		http, _ := out["http"].(map[string]any)
		if http == nil {
			http = map[string]any{}
			out["http"] = http
		}
		routers, _ := http["routers"].(map[string]any)
		if routers == nil {
			routers = map[string]any{}
			http["routers"] = routers
		}
		routers["activation-"+s.Revision] = map[string]any{"entryPoints": []string{"traefik"}, "rule": "Path(`/__astilba_activation`)", "service": "api@internal"}
	}
	return deployment.RouteFile(out)
}
func atomicRouteFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("ingress directory must be private")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("ingress file must be regular")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".route-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// awaitRoutes waits for the running proxy to report a published file enabled.
func (e *Engine) awaitRoutes(ctx context.Context, s routeSnapshot, raw []byte) error {
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		err := e.verifyRoutes(deadline, s)
		if err == nil {
			current, readErr := os.ReadFile(e.routePath())
			if readErr != nil || !bytes.Equal(current, raw) {
				return errors.New("published route file changed before activation")
			}
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("route runtime verification failed: %w", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// QualifyRouteBackends moves a host off the bare backend names an older agent published.
// No command may arrive for days after an upgrade, and until this runs another unit on
// the host can answer for those names. It changes only the route file, so it never
// restarts the proxy, whatever this agent would now choose to run: the routes and the
// proxy's networks are already those the journal records. A failure restores the previous
// file and is tried again later. The revision is derived so that a restart after the file
// was written, before the journal, finds the same file here.
func (e *Engine) QualifyRouteBackends(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	state, err := e.Journal.routing()
	if err != nil {
		return err
	}
	current := state.Current
	if state.Pending != nil || current.QualifiedBackends || len(current.Sets) == 0 {
		return nil
	}
	if held, err := e.Journal.recoveryHeld(); err != nil || held {
		return err
	}
	next := current
	next.QualifiedBackends = true
	next.Revision = uuid.NewSHA1(uuid.NameSpaceOID, []byte("qualified-backends/"+current.Revision)).String()
	published, err := renderSnapshot(next)
	if err != nil {
		return err
	}
	// Drift is left for the owner's explicit reconciliation, which also converts.
	observed := current
	if raw, err := os.ReadFile(e.routePath()); err == nil && bytes.Equal(raw, published) {
		observed = next
	}
	if drift, err := e.ingressDrift(ctx, routeState{Current: observed}); err != nil || drift {
		return err
	}
	if err = e.replaceRouteFile(ctx, next); err != nil {
		if restore := e.replaceRouteFile(ctx, current); restore != nil {
			return fmt.Errorf("restoring routes after a failed conversion: %w", restore)
		}
		return err
	}
	state.Current = next
	return e.Journal.saveRouting(state)
}

func (e *Engine) replaceRouteFile(ctx context.Context, s routeSnapshot) error {
	raw, err := renderSnapshot(s)
	if err != nil {
		return err
	}
	if err = atomicRouteFile(e.routePath(), raw); err != nil {
		return err
	}
	e.point("route-published")
	return e.awaitRoutes(ctx, s, raw)
}

// qualifyRouteBackends logs a failure and leaves it to the next attempt: converting must
// not stop the agent deploying.
func (e *Engine) qualifyRouteBackends(ctx context.Context) {
	if err := e.QualifyRouteBackends(ctx); err != nil {
		slog.Error("route backend conversion failed", "error", err)
	}
}
