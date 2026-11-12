package execution

import (
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

const reviewCompose = `services:
  agent:
    image: monitor:latest
    privileged: true
    command: [run, --watch]
    entrypoint: [/bin/monitor]
    environment:
      LEVEL: info
      TOKEN: ${TOKEN}
    volumes: ['./agent.conf:/etc/agent.conf:ro']
    configs: [agent_rules]
    networks: [ops]
  tuner:
    image: tuner:1
    cap_add: [NET_ADMIN]
    command: [tune]
    networks: [ops]
  web:
    image: nginx:latest
    ports: [127.0.0.1:9011:80]
    networks: [front]
networks:
  ops: {}
  front: {}
configs:
  agent_rules:
    file: rules.txt
`

type reviewSource struct {
	compose, conf, rules, token string
}

func (r reviewSource) spec(t *testing.T, namespace uuid.UUID) domain.Specification {
	t.Helper()
	files := []composeproject.File{{Path: "compose.yaml", Content: r.compose}, {Path: "agent.conf", Content: r.conf}, {Path: "rules.txt", Content: r.rules}}
	raw, _ := stablejson.Marshal(domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{{Name: "TOKEN", Value: &r.token}}, Compose: &composeproject.Bundle{Namespace: namespace, Entrypoints: []string{"compose.yaml"}, Files: files}})
	spec, _, err := domain.ParseSpec(raw)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	return spec
}

func reviewNote(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("deployed without review")
	}
	note := automaticReviewNote(err, "")
	if note == "" {
		t.Fatalf("no job-history note for %v", err)
	}
	return note
}

func TestAutomaticChangesStopForChangedPrivilegedServices(t *testing.T) {
	namespace := uuid.New()
	base := reviewSource{compose: reviewCompose, conf: "mode=strict\n", rules: "allow all\n", token: "first"}
	previous := base.spec(t, namespace)
	t.Run("identical definition carries forward its approval", func(t *testing.T) {
		_, err := automaticChanges(previous, base.spec(t, namespace))
		if err != nil {
			t.Fatalf("identical privileged definition was not deployable: %v", err)
		}
	})
	t.Run("a stateless port-only service still carries forward", func(t *testing.T) {
		next := base
		next.compose = strings.ReplaceAll(reviewCompose, "nginx:latest", "nginx:2")
		_, err := automaticChanges(previous, next.spec(t, namespace))
		if err != nil {
			t.Fatalf("port-only change was refused: %v", err)
		}
	})
	edit := func(old, replacement string) func(reviewSource) reviewSource {
		return func(r reviewSource) reviewSource {
			r.compose = strings.Replace(r.compose, old, replacement, 1)
			return r
		}
	}
	for _, tc := range []struct {
		name, service string
		change        func(reviewSource) reviewSource
	}{
		{"image", "agent", edit("monitor:latest", "monitor:2")},
		{"image pinned to a digest", "agent", edit("monitor:latest", "monitor@sha256:"+strings.Repeat("a", 64))},
		{"command", "agent", edit("[run, --watch]", "[run, --watch, --shell]")},
		{"entrypoint", "agent", edit("[/bin/monitor]", "[/bin/sh]")},
		{"environment", "agent", edit("LEVEL: info", "LEVEL: debug")},
		{"interpolated variable", "agent", func(r reviewSource) reviewSource { r.token = "second"; return r }},
		{"bind-mounted file", "agent", func(r reviewSource) reviewSource { r.conf = "mode=open\n"; return r }},
		{"config file", "agent", func(r reviewSource) reviewSource { r.rules = "deny none\n"; return r }},
		{"user", "agent", edit("    privileged: true\n", "    privileged: true\n    user: root\n")},
		{"removal", "agent", func(r reviewSource) reviewSource {
			r.compose = strings.Replace(r.compose, reviewCompose[strings.Index(reviewCompose, "  agent:"):strings.Index(reviewCompose, "  tuner:")], "", 1)
			r.compose = strings.Replace(r.compose, "configs:\n  agent_rules:\n    file: rules.txt\n", "", 1)
			return r
		}},
		{"cap_add service command", "tuner", edit("[tune]", "[tune, --all]")},
		{"cap_add service image", "tuner", edit("tuner:1", "tuner:2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := automaticChanges(previous, tc.change(base).spec(t, namespace))
			note := reviewNote(t, err)
			if !strings.Contains(note, tc.service+" has host-level access") {
				t.Fatalf("note does not explain the privileged review: %q", note)
			}
		})
	}
	t.Run("new privileged service is a new permission", func(t *testing.T) {
		next := base
		next.compose = strings.Replace(reviewCompose, "  web:", "  shell:\n    image: busybox:1\n    privileged: true\n  web:", 1)
		note := reviewNote(t, func() error { _, err := automaticChanges(previous, next.spec(t, namespace)); return err }())
		if !strings.Contains(note, "shell: privileged container") {
			t.Fatalf("note does not name the new permission: %q", note)
		}
	})
}

func TestAutomaticImagesStopForMovedTagsOnOtherServices(t *testing.T) {
	namespace := uuid.New()
	base := reviewSource{compose: reviewCompose, conf: "x", rules: "y", token: "z"}
	spec := base.spec(t, namespace)
	elevated, err := automaticChanges(spec, spec)
	if err != nil || len(elevated) != 2 {
		t.Fatalf("privileged services were not identified: %v %v", elevated, err)
	}
	id := func(name string) uuid.UUID { return composeproject.ResourceID(namespace, "service", name) }
	reviewed := map[string]string{id("agent").String(): "monitor@sha256:a1", id("tuner").String(): "tuner@sha256:t1", id("web").String(): "nginx@sha256:w1"}
	with := func(name, image string) map[string]string {
		out := map[string]string{}
		for k, v := range reviewed {
			out[k] = v
		}
		out[id(name).String()] = image
		return out
	}
	if err := automaticImages(spec, spec, elevated, uuid.Nil, reviewed, reviewed); err != nil {
		t.Fatal("unchanged digests were refused", err)
	}
	t.Run("moved tag on another stateless service stops with a note", func(t *testing.T) {
		note := reviewNote(t, automaticImages(spec, spec, elevated, uuid.Nil, reviewed, with("web", "nginx@sha256:w2")))
		if !strings.Contains(note, "image tag for web") || !strings.Contains(note, "Pinning the image by digest") {
			t.Fatalf("note does not explain the moved tag: %q", note)
		}
		if err := automaticImages(spec, spec, elevated, id("tuner"), reviewed, with("web", "nginx@sha256:w2")); err == nil {
			t.Fatal("another service's moved tag deployed with this run's build")
		}
	})
	t.Run("run's own build image deploys", func(t *testing.T) {
		if err := automaticImages(spec, spec, elevated, id("web"), reviewed, with("web", "registry.example/web@sha256:new")); err != nil {
			t.Fatal("the run's own image was refused", err)
		}
	})
	t.Run("an image reference the commit edited deploys", func(t *testing.T) {
		next := base
		next.compose = strings.ReplaceAll(reviewCompose, "nginx:latest", "nginx:2")
		if err := automaticImages(spec, next.spec(t, namespace), elevated, uuid.Nil, reviewed, with("web", "nginx@sha256:w2")); err != nil {
			t.Fatal("the commit's own image edit was refused", err)
		}
	})
	t.Run("an implied latest tag is the same reference", func(t *testing.T) {
		next := base
		next.compose = strings.ReplaceAll(reviewCompose, "nginx:latest", "docker.io/library/nginx")
		if err := automaticImages(spec, next.spec(t, namespace), elevated, uuid.Nil, reviewed, with("web", "nginx@sha256:w2")); err == nil {
			t.Fatal("respelling the reference let a moved tag deploy")
		}
	})
	for _, name := range []string{"agent", "tuner"} {
		t.Run("moved tag on privileged "+name, func(t *testing.T) {
			for _, target := range []uuid.UUID{uuid.Nil, id(name)} {
				note := reviewNote(t, automaticImages(spec, spec, elevated, target, reviewed, with(name, "moved@sha256:m")))
				if !strings.Contains(note, name+" has host-level access") {
					t.Fatalf("note does not explain the privileged digest: %q", note)
				}
			}
		})
	}
	t.Run("a service without a reviewed image stops", func(t *testing.T) {
		missing := with("web", "")
		for _, images := range [][2]map[string]string{{missing, missing}, {missing, reviewed}, {reviewed, missing}} {
			note := reviewNote(t, automaticImages(spec, spec, elevated, uuid.Nil, images[0], images[1]))
			if !strings.Contains(note, "web has no single resolved image") {
				t.Fatalf("note does not explain the missing image: %q", note)
			}
		}
	})
	t.Run("a new service is the run's change", func(t *testing.T) {
		next := base
		next.compose = strings.Replace(reviewCompose, "  web:", "  extra:\n    image: busybox:latest\n  web:", 1)
		if err := automaticImages(spec, next.spec(t, namespace), elevated, uuid.Nil, reviewed, with("extra", "busybox@sha256:b")); err != nil {
			t.Fatal("a new stateless service was refused", err)
		}
	})
	t.Run("visual services", func(t *testing.T) {
		latest, other := "nginx:latest", "worker:latest"
		built := domain.Application{ID: uuid.New(), Name: "api", Source: domain.ApplicationSource{Kind: "image", Image: &latest}}
		worker := domain.Application{ID: uuid.New(), Name: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &other}}
		visual := domain.Specification{Applications: []domain.Application{built, worker}}
		applied := map[string]string{built.ID.String(): "nginx@sha256:1", worker.ID.String(): "worker@sha256:1"}
		if err := automaticImages(visual, visual, nil, built.ID, applied, map[string]string{built.ID.String(): "nginx@sha256:2", worker.ID.String(): "worker@sha256:1"}); err != nil {
			t.Fatal("the run's own image was refused", err)
		}
		note := reviewNote(t, automaticImages(visual, visual, nil, built.ID, applied, map[string]string{built.ID.String(): "nginx@sha256:2", worker.ID.String(): "worker@sha256:2"}))
		if !strings.Contains(note, "image tag for worker") {
			t.Fatalf("note does not name the moved service: %q", note)
		}
	})
}

func TestPlanImagesIncludesCrossHostUnitsAndRefusesDisagreement(t *testing.T) {
	a, b := uuid.NewString(), uuid.NewString()
	plan := deployment.Plan{Images: map[string]string{}, Distributed: &deployment.DistributedPlan{Units: []deployment.DistributedUnit{
		{Plan: deployment.Plan{Images: map[string]string{a: "a@sha256:1"}}},
		{Plan: deployment.Plan{Images: map[string]string{a: "a@sha256:1", b: "b@sha256:1"}}},
	}}}
	if got := planImages(plan); got[a] != "a@sha256:1" || got[b] != "b@sha256:1" {
		t.Fatalf("unit images were not collected: %v", got)
	}
	plan.Distributed.Units[1].Plan.Images[a] = "a@sha256:2"
	if got := planImages(plan); got[a] != "" {
		t.Fatalf("disagreeing units produced a reviewed image: %v", got)
	}
}

// Routes decide how a service is reached, which matters for host-level access but not for the
// storage compatibility that the stateful rule protects.
func TestAutomaticChangesCompareRoutesOnlyForPrivilegedServices(t *testing.T) {
	namespace := uuid.New()
	compose := func(settings, domain string) reviewSource {
		return reviewSource{compose: "services:\n  app:\n    image: app:1\n" + settings + "volumes:\n  data: {}\nx-astilba:\n  routes:\n    - name: public\n      service: app\n      protocol: http\n      domain: " + domain + "\n      port: 8080\n      tls: 'off'\n", conf: "x", rules: "y", token: "z"}
	}
	stateful := "    volumes: [data:/data]\n"
	if _, err := automaticChanges(compose(stateful, "a.example.test").spec(t, namespace), compose(stateful, "b.example.test").spec(t, namespace)); err != nil {
		t.Fatal("a route change on a stateful service was refused", err)
	}
	privileged := "    privileged: true\n"
	_, err := automaticChanges(compose(privileged, "a.example.test").spec(t, namespace), compose(privileged, "b.example.test").spec(t, namespace))
	if note := reviewNote(t, err); !strings.Contains(note, "app has host-level access") {
		t.Fatalf("note does not explain the privileged review: %q", note)
	}
}

// Manual plan preparation and automatic deployment share this answer to whether a privileged
// service still stands as reviewed.
func TestReviewElevatedServicesReportsEachPrivilegedService(t *testing.T) {
	namespace := uuid.New()
	base := reviewSource{compose: reviewCompose, conf: "x", rules: "y", token: "z"}
	reviewed := base.spec(t, namespace)
	id := func(name string) uuid.UUID { return composeproject.ResourceID(namespace, "service", name) }
	same, err := reviewElevatedServices(reviewed, base.spec(t, namespace))
	if err != nil || len(same) != 2 || !same[id("agent")].Unchanged || !same[id("tuner")].Unchanged {
		t.Fatalf("identical privileged services do not stand: %v %v", same, err)
	}
	changed := base
	changed.compose = strings.Replace(reviewCompose, "[tune]", "[tune, --all]", 1)
	got, err := reviewElevatedServices(reviewed, changed.spec(t, namespace))
	if err != nil || !got[id("agent")].Unchanged || got[id("tuner")].Unchanged || got[id("tuner")].Name != "tuner" {
		t.Fatalf("a changed cap_add service was not told apart: %v %v", got, err)
	}
	if _, ok := got[id("web")]; ok {
		t.Fatal("a service without host-level access was reported")
	}
	// A first deployment, or a switch from visual services, has no reviewed Compose files.
	for _, previous := range []domain.Specification{{}, {Applications: []domain.Application{}}} {
		first, err := reviewElevatedServices(previous, reviewed)
		if err != nil || len(first) != 2 || first[id("agent")].Unchanged || first[id("tuner")].Unchanged {
			t.Fatalf("privileged services without a reviewed definition stood: %v %v", first, err)
		}
	}
	removed, err := reviewElevatedServices(reviewed, domain.Specification{})
	if err != nil || len(removed) != 2 || removed[id("agent")].Unchanged {
		t.Fatalf("privileged services removed by a switch to visual services stood: %v %v", removed, err)
	}
	if !resolvedImageStands(map[string]string{id("agent").String(): "m@sha256:1"}, map[string]string{id("agent").String(): "m@sha256:1"}, id("agent")) ||
		resolvedImageStands(map[string]string{id("agent").String(): "m@sha256:1"}, map[string]string{id("agent").String(): "m@sha256:2"}, id("agent")) ||
		resolvedImageStands(map[string]string{}, map[string]string{}, id("agent")) {
		t.Fatal("resolved images were not compared as reviewed")
	}
}
