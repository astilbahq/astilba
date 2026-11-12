package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// peerDaemon answers the Docker API calls eligibility, peer checks and a scheduled job make,
// from a fixed set of containers.
type peerDaemon struct {
	mu         sync.Mutex
	containers []map[string]any
	created    []map[string]any
}

type peerContainer struct {
	id, name string
	labels   map[string]string
	image    string
	host     map[string]any
	networks []string
	volumes  []string
}

func (d *peerDaemon) add(c peerContainer) {
	networks := map[string]any{}
	for _, name := range c.networks {
		networks[name] = map[string]any{}
	}
	mounts := []any{}
	for _, name := range c.volumes {
		mounts = append(mounts, map[string]any{"Type": "volume", "Name": name, "RW": true})
	}
	if c.host == nil {
		c.host = map[string]any{}
	}
	d.containers = append(d.containers, map[string]any{"Id": c.id, "Name": "/" + c.name, "Image": c.image, "Config": map[string]any{"Image": c.image, "Labels": c.labels}, "HostConfig": c.host, "NetworkSettings": map[string]any{"Networks": networks}, "Mounts": mounts, "State": map[string]any{"Running": true, "Status": "running"}})
}

func (d *peerDaemon) find(ref string) map[string]any {
	for _, c := range d.containers {
		if c["Id"] == ref || c["Name"] == "/"+ref {
			return c
		}
	}
	return nil
}

func (d *peerDaemon) matches(c map[string]any, filters map[string]map[string]bool) bool {
	labels, _ := c["Config"].(map[string]any)["Labels"].(map[string]string)
	for want := range filters["label"] {
		key, value, _ := strings.Cut(want, "=")
		if labels[key] != value {
			return false
		}
	}
	for want := range filters["network"] {
		if _, ok := c["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[want]; !ok {
			return false
		}
	}
	for want := range filters["volume"] {
		if !slices.ContainsFunc(c["Mounts"].([]any), func(m any) bool { return m.(map[string]any)["Name"] == want }) {
			return false
		}
	}
	return true
}

func (d *peerDaemon) client(t *testing.T) *client.Client {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path[strings.Index(r.URL.Path, "/containers"):]
		switch {
		case path == "/containers/json":
			filters := map[string]map[string]bool{}
			if raw := r.URL.Query().Get("filters"); raw != "" {
				if err := json.Unmarshal([]byte(raw), &filters); err != nil {
					t.Errorf("filters %q: %v", raw, err)
				}
			}
			out := []any{}
			for _, c := range d.containers {
				if d.matches(c, filters) {
					out = append(out, map[string]any{"Id": c["Id"], "Names": []string{c["Name"].(string)}, "Labels": c["Config"].(map[string]any)["Labels"], "Mounts": c["Mounts"]})
				}
			}
			stablejson.Encode(w, out)
		case path == "/containers/create":
			var body map[string]any
			json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &body)
			d.created = append(d.created, body)
			labels := map[string]string{}
			for k, v := range body["Labels"].(map[string]any) {
				labels[k] = v.(string)
			}
			d.containers = append(d.containers, map[string]any{"Id": "job", "Name": "/" + r.URL.Query().Get("name"), "Config": map[string]any{"Labels": labels}, "HostConfig": body["HostConfig"], "NetworkSettings": map[string]any{"Networks": map[string]any{}}, "Mounts": []any{}, "State": map[string]any{"Running": false, "Status": "exited", "ExitCode": 0}})
			w.WriteHeader(201)
			fmt.Fprint(w, `{"Id":"job","Warnings":[]}`)
		case strings.HasSuffix(path, "/start") || strings.HasSuffix(path, "/stop") || r.Method == http.MethodDelete:
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/json"):
			c := d.find(strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json"))
			if c == nil {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"message":"absent"}`)
				return
			}
			stablejson.Encode(w, c)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"absent"}`)
		}
	})
	transport := scheduleTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})
	docker, err := client.New(client.WithHost("http://docker-fixture"), client.WithHTTPClient(&http.Client{Transport: transport}), client.WithAPIVersion("1.56"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	return docker
}

// peerCompose has a privileged service with a volume peer and a network peer, and a service
// that shares a network only with a peer.
const peerCompose = `services:
  dind:
    image: docker:dind
    privileged: true
    volumes: [certs:/certs]
    networks: [builder]
  client:
    image: docker:cli
    volumes: [certs:/certs/client:ro]
    networks: [apps]
  runner:
    image: runner:1
    networks: [builder]
  web:
    image: nginx:1
    networks: [apps]
volumes:
  certs: {}
networks:
  builder: {}
  apps: {}
`

// composePlan journals a plan for peerCompose, and returns it with each service's ID by name.
func composePlan(t *testing.T, j *Journal, plan deployment.Plan) (deployment.Plan, map[string]uuid.UUID) {
	t.Helper()
	raw, _ := stablejson.Marshal(domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Compose: &composeproject.Bundle{Namespace: plan.UnitID, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: peerCompose}}}})
	spec, _, err := domain.ParseSpec(raw)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	plan.Specification = spec
	plan = plan.WithHash()
	if err = j.RecordUnitPlan(plan); err != nil {
		t.Fatal(err)
	}
	ids := map[string]uuid.UUID{}
	for _, app := range spec.Applications {
		ids[app.Name] = app.ID
	}
	return plan, ids
}

// A terminal is refused in a privileged service's volume and network peers, as the journaled
// plan describes them, even before the daemon runs the privileged service.
func TestTroubleshootingRefusesPeersOfPrivilegedServicesInThePlan(t *testing.T) {
	j, plan := testVisualJournal(t)
	plan, ids := composePlan(t, j, plan)
	d := &peerDaemon{}
	for name, id := range ids {
		if name != "dind" {
			d.add(peerContainer{id: name, name: deployment.UnitName(id), labels: visualLabels(plan, id), host: map[string]any{"Runtime": "runc"}})
		}
	}
	e := &Engine{Journal: j, Docker: d.client(t)}
	for name, want := range map[string]bool{"client": false, "runner": false, "web": true} {
		_, err := e.TroubleshootingTarget(context.Background(), plan.UnitID, ids[name], 1)
		if got := err == nil; got != want {
			t.Errorf("%s: eligible %v, want %v (%v)", name, got, want, err)
		}
		if err != nil && !errors.Is(err, ErrTroubleshootingIneligible) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The agent also refuses a container the daemon runs beside one with authority over the host,
// on a shared network or named volume, whatever the plan says.
func TestTroubleshootingRefusesRuntimePeersWithHostAuthority(t *testing.T) {
	j, plan := testVisualJournal(t)
	app := uuid.New()
	image := execution.TestImage
	plan.Specification.Applications = []domain.Application{{ID: app, Name: "web", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}
	plan = plan.WithHash()
	if err := j.RecordUnitPlan(plan); err != nil {
		t.Fatal(err)
	}
	network, volume := deployment.NetworkName(plan.UnitID), deployment.VolumeName(uuid.New())
	for name, test := range map[string]struct {
		peer peerContainer
		want bool
	}{
		"no peer":                 {peerContainer{id: "peer", name: "peer", networks: []string{"elsewhere"}, volumes: []string{"other"}, host: map[string]any{"Privileged": true}}, true},
		"ordinary network peer":   {peerContainer{id: "peer", name: "peer", networks: []string{network}}, true},
		"privileged network peer": {peerContainer{id: "peer", name: "peer", networks: []string{network}, host: map[string]any{"Privileged": true}}, false},
		"privileged volume peer":  {peerContainer{id: "peer", name: "peer", volumes: []string{volume}, host: map[string]any{"Privileged": true}}, false},
		"capability volume peer":  {peerContainer{id: "peer", name: "peer", volumes: []string{volume}, host: map[string]any{"CapAdd": []string{"SYS_ADMIN"}}}, false},
		"device network peer":     {peerContainer{id: "peer", name: "peer", networks: []string{network}, host: map[string]any{"Devices": []any{map[string]any{"PathOnHost": "/dev/kvm"}}}}, false},
		"host PID network peer":   {peerContainer{id: "peer", name: "peer", networks: []string{network}, host: map[string]any{"PidMode": "host"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			d := &peerDaemon{}
			d.add(peerContainer{id: "target", name: deployment.UnitName(app), labels: visualLabels(plan, app), host: map[string]any{"Runtime": "runc"}, networks: []string{network}, volumes: []string{volume}})
			d.add(test.peer)
			e := &Engine{Journal: j, Docker: d.client(t)}
			_, err := e.TroubleshootingTarget(context.Background(), plan.UnitID, app, 1)
			if got := err == nil; got != test.want {
				t.Fatalf("eligible %v, want %v (%v)", got, test.want, err)
			}
		})
	}
}

// A deployment that makes a service elevated ends the shells in it before its Compose run, and
// refuses new ones; shells elsewhere carry on.
func TestDeploymentEndsShellsInServicesItMakesElevated(t *testing.T) {
	j, plan := testVisualJournal(t)
	elevated, ids := composePlan(t, j, plan)
	e := &Engine{Journal: j}
	end, release, ok := e.admitShell(plan.UnitID, ids["runner"])
	other, releaseOther, otherOK := e.admitShell(plan.UnitID, ids["web"])
	if !ok || !otherOK {
		t.Fatal("shells refused before any deployment")
	}
	defer releaseOther()
	fenced := make(chan error, 1)
	go func() { fenced <- e.fenceTerminals(context.Background(), elevated) }()
	select {
	case <-end:
	case <-time.After(5 * time.Second):
		t.Fatal("the shell in the new peer was not asked to end")
	}
	select {
	case err := <-fenced:
		t.Fatalf("the deployment went ahead before the shell ended: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-fenced; err != nil {
		t.Fatal(err)
	}
	select {
	case <-other:
		t.Fatal("a shell in a service that is not elevated was ended")
	default:
	}
	if _, _, ok = e.admitShell(plan.UnitID, ids["client"]); ok {
		t.Fatal("a shell opened in a peer while its plan was being deployed")
	}
	if !e.incomingRefuses(plan.UnitID, ids["runner"]) || e.incomingRefuses(plan.UnitID, ids["web"]) {
		t.Fatal("eligibility does not follow the plan being deployed")
	}
	// A later plan without the privileged service lets shells open again.
	if err := e.fenceTerminals(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if _, release, ok = e.admitShell(plan.UnitID, ids["runner"]); !ok {
		t.Fatal("a shell stayed refused after the service stopped being elevated")
	}
	release()
}

// A deployment fails rather than start a privileged service beside a shell that will not end.
func TestDeploymentFailsWhenAShellWillNotEnd(t *testing.T) {
	j, plan := testVisualJournal(t)
	elevated, ids := composePlan(t, j, plan)
	e := &Engine{Journal: j}
	if _, _, ok := e.admitShell(plan.UnitID, ids["client"]); !ok {
		t.Fatal("shell refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := e.fenceTerminals(ctx, elevated); err == nil {
		t.Fatal("the deployment went ahead with the shell still running")
	}
}

// jobFixture runs a scheduled job for a visual worker whose container the daemon runs with the
// given networks and its own volume, then lets setup change the daemon before the unit's
// expected state is recorded.
func jobFixture(t *testing.T, networks []string, setup func(d *peerDaemon, p backup.Policy)) (*Engine, *peerDaemon, *scheduleState) {
	t.Helper()
	j, p := testVisualJournal(t)
	app := uuid.New()
	image := execution.TestImage
	p.Specification.Applications = []domain.Application{{ID: app, Name: "Worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Command: []string{"sleep", "86400"}}}
	p.Images = map[string]string{app.String(): image}
	p = p.WithHash()
	policy := backup.Policy{ID: uuid.New(), Name: "Worker", WorkspaceID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, ResourceID: app, Generation: 1, Method: "container-job", Command: []string{"true"}, Schedule: backup.Schedule{Kind: "interval", IntervalMinutes: 1, Timezone: "UTC"}, TimeoutSeconds: 1, Retain: 1, Enabled: true, Plan: p}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	d := &peerDaemon{}
	d.add(peerContainer{id: "source", name: deployment.UnitName(app), labels: visualLabels(p, app), image: image, networks: networks, volumes: []string{deployment.VolumeName(app)}})
	if setup != nil {
		setup(d, policy)
	}
	e := &Engine{Journal: j, Docker: d.client(t)}
	_, expected, err := e.observeUnit(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.saveUnit(unitState{Plan: p, Expected: expected}); err != nil {
		t.Fatal(err)
	}
	return e, d, &scheduleState{Installation: backup.Installation{Policy: policy}}
}

// A job joins exactly its source's networks, without the service's aliases, and none when its
// source has none.
func TestScheduledJobJoinsExactlyItsSourcesNetworks(t *testing.T) {
	onUnit := func(d *peerDaemon, p backup.Policy) {
		d.containers[0]["NetworkSettings"] = map[string]any{"Networks": map[string]any{deployment.NetworkName(p.Plan.UnitID): map[string]any{"Aliases": []string{"worker"}}}}
	}
	for name, setup := range map[string]func(*peerDaemon, backup.Policy){"unit network": onUnit, "no network": nil} {
		t.Run(name, func(t *testing.T) {
			e, d, s := jobFixture(t, nil, setup)
			unit := deployment.NetworkName(s.Installation.Policy.Plan.UnitID)
			if err := e.runContainerJob(context.Background(), s, &backup.Run{ID: uuid.New()}); err != nil {
				t.Fatal(err)
			}
			if len(d.created) != 1 {
				t.Fatalf("created %d containers", len(d.created))
			}
			mode := d.created[0]["HostConfig"].(map[string]any)["NetworkMode"]
			endpoints := map[string]any{}
			if config, ok := d.created[0]["NetworkingConfig"].(map[string]any); ok && config["EndpointsConfig"] != nil {
				endpoints = config["EndpointsConfig"].(map[string]any)
			}
			if setup == nil {
				if mode != "none" || len(endpoints) != 0 {
					t.Fatalf("a job of a source without networks joined %v %v", mode, endpoints)
				}
				return
			}
			if mode != unit || !slices.Equal(slices.Sorted(maps.Keys(endpoints)), []string{unit}) {
				t.Fatalf("job joined %v %v, want only %s", mode, slices.Collect(maps.Keys(endpoints)), unit)
			}
			if aliases := endpoints[unit].(map[string]any)["Aliases"]; aliases != nil {
				t.Fatalf("job took the service's aliases %v", aliases)
			}
		})
	}
	t.Run("a network the plan does not own", func(t *testing.T) {
		e, d, s := jobFixture(t, []string{deployment.NetworkName(uuid.New())}, nil)
		if err := e.runContainerJob(context.Background(), s, &backup.Run{ID: uuid.New()}); err == nil || len(d.created) != 0 {
			t.Fatalf("a job joined a network its plan does not own: %v", err)
		}
	})
}

// A job is refused when its source shares a network or named volume with a container that has
// authority over the host, or has it itself.
func TestScheduledJobRefusesSourcesBesidePrivilegedContainers(t *testing.T) {
	privileged := map[string]any{"Privileged": true}
	for name, setup := range map[string]func(d *peerDaemon, p backup.Policy){
		"network peer": func(d *peerDaemon, p backup.Policy) {
			network := deployment.NetworkName(p.Plan.UnitID)
			d.containers[0]["NetworkSettings"] = map[string]any{"Networks": map[string]any{network: map[string]any{}}}
			d.add(peerContainer{id: "dind", name: "dind", networks: []string{network}, host: privileged})
		},
		"volume peer": func(d *peerDaemon, p backup.Policy) {
			d.add(peerContainer{id: "dind", name: "dind", volumes: []string{deployment.VolumeName(p.ResourceID)}, host: privileged})
		},
		"source with added capabilities": func(d *peerDaemon, _ backup.Policy) {
			d.containers[0]["HostConfig"] = map[string]any{"CapAdd": []string{"NET_ADMIN"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, d, s := jobFixture(t, nil, setup)
			err := e.runContainerJob(context.Background(), s, &backup.Run{ID: uuid.New()})
			if !errors.Is(err, errElevatedJob) || len(d.created) != 0 {
				t.Fatalf("job ran beside a privileged container: %v", err)
			}
		})
	}
}

// A job whose source the plan treats as elevated is refused before anything about the host is
// read, ahead of the refusal of Compose sources.
func TestScheduledJobRefusesPlanElevatedSources(t *testing.T) {
	j, plan := testVisualJournal(t)
	plan, ids := composePlan(t, j, plan)
	e := &Engine{Journal: j, Docker: (&peerDaemon{}).client(t)}
	for name, want := range map[string]bool{"client": true, "runner": true, "web": false} {
		p := backup.Policy{ServerID: plan.ServerID, Method: "container-job", ResourceID: ids[name], Plan: plan}
		err := e.checkBackupSource(context.Background(), p)
		if got := errors.Is(err, errElevatedJob); got != want {
			t.Errorf("%s: refused as elevated %v, want %v (%v)", name, got, want, err)
		}
	}
}
