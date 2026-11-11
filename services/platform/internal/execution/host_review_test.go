package execution

import (
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func TestElevatedServicesStandOnlyAsApplied(t *testing.T) {
	namespace, server := uuid.New(), uuid.New()
	base := reviewSource{compose: reviewCompose, conf: "x", rules: "y", token: "z"}
	id := func(name string) string { return composeproject.ResourceID(namespace, "service", name).String() }
	images := map[string]string{id("agent"): "monitor@sha256:a1", id("tuner"): "tuner@sha256:t1", id("web"): "nginx@sha256:w1"}
	applied := deployment.Plan{ID: uuid.New(), ServerID: server, Specification: base.spec(t, namespace), Images: images}
	plan := func(source reviewSource, on uuid.UUID, image map[string]string) deployment.Plan {
		resolved := map[string]string{}
		for k, v := range images {
			resolved[k] = v
		}
		for k, v := range image {
			resolved[id(k)] = v
		}
		return deployment.Plan{ID: uuid.New(), ServerID: on, Specification: source.spec(t, namespace), Images: resolved}
	}
	moved := base
	moved.compose = strings.Replace(reviewCompose, "nginx:latest", "nginx:2", 1)
	edited := base
	edited.conf = "changed"
	for _, tc := range []struct {
		name  string
		plan  deployment.Plan
		prior deployment.Plan
		stand bool
	}{
		{"redeploy as applied", plan(base, server, nil), applied, true},
		{"a service without host access changes", plan(moved, server, map[string]string{"web": "nginx@sha256:w2"}), applied, true},
		{"privileged service resolves to another image", plan(base, server, map[string]string{"agent": "monitor@sha256:a2"}), applied, false},
		{"privileged service's file changes", plan(edited, server, nil), applied, false},
		{"another server", plan(base, uuid.New(), nil), applied, false},
		{"first deployment", plan(base, server, nil), deployment.Plan{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := unapprovedElevatedServices(tc.prior, tc.plan)
			if stand := len(changed) == 0; err != nil || stand != tc.stand {
				t.Fatalf("stand = %v, %v; want %v", stand, err, tc.stand)
			}
		})
	}
}

func TestPeersOfPrivilegedServicesNeedReview(t *testing.T) {
	const project = `services:
  dind:
    image: docker:dind
    privileged: true
DIND  runner:
    image: runner:1
RUNNER  web:
    image: nginx:latest
    ports: [127.0.0.1:9011:80]
    networks: [front]
networks:
  ops: {}
  front: {}
volumes:
  work: {}
`
	const ops = "    networks: [ops]\n    volumes: [work:/work]\n"
	for _, tc := range []struct {
		name, dind, runner, routed string
		reviewed                   bool
	}{
		{"network peer", ops, "    networks: [ops]\n", "", true},
		{"volume peer", ops, "    networks: [front]\n    volumes: [work:/work]\n", "", true},
		{"isolated service", ops, "    networks: [front]\n", "", false},
		{"default network peer", "", "", "", true},
		{"service on the default network beside a privileged service on another", ops, "", "", false},
		// Deployment attaches a route's target to the default network as well.
		{"routed privileged service", ops, "", "dind", true},
		{"routed service beside a privileged service on the default network", "", "    networks: [front]\n", "runner", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			namespace, server := uuid.New(), uuid.New()
			source := reviewSource{compose: strings.NewReplacer("DIND", tc.dind, "RUNNER", tc.runner).Replace(project), conf: "x", rules: "y", token: "z"}
			next := source
			next.compose = strings.Replace(source.compose, "runner:1", "runner:2", 1)
			reviewedSpec, nextSpec := source.spec(t, namespace), next.spec(t, namespace)
			if tc.routed != "" {
				route := []domain.Route{{ID: uuid.New(), TargetID: composeproject.ResourceID(namespace, "service", tc.routed), Protocol: "http", Domain: "app.example.test", Port: 80, TLS: "off"}}
				reviewedSpec.Routes, nextSpec.Routes = route, route
			}
			images := func(spec domain.Specification) map[string]string {
				out := map[string]string{}
				for _, app := range spec.Applications {
					out[app.ID.String()] = *app.Source.Image + "@sha256:" + strings.Repeat("a", 64)
				}
				return out
			}
			applied := deployment.Plan{ID: uuid.New(), ServerID: server, Specification: reviewedSpec, Images: images(reviewedSpec)}
			changed, err := unapprovedElevatedServices(applied, deployment.Plan{ID: uuid.New(), ServerID: server, Specification: nextSpec, Images: images(nextSpec)})
			if err != nil || len(changed) == 0 == tc.reviewed || tc.reviewed && !slices.Contains(changed, "runner") {
				t.Fatalf("manual redeploy needs review for %v, %v; reviewed %v", changed, err, tc.reviewed)
			}
			_, err = automaticChanges(reviewedSpec, nextSpec)
			if (err != nil) != tc.reviewed || tc.reviewed && !strings.Contains(automaticReviewNote(err, ""), "runner has host-level access") {
				t.Fatalf("automatic deployment refused = %v, want %v", err, tc.reviewed)
			}
		})
	}
	t.Run("port-only project", func(t *testing.T) {
		namespace, server := uuid.New(), uuid.New()
		source := reviewSource{compose: "services:\n  web:\n    image: nginx:1\n    ports: [127.0.0.1:9011:80]\n  worker:\n    image: worker:1\n", conf: "x", rules: "y", token: "z"}
		next := source
		next.compose = strings.Replace(source.compose, "worker:1", "worker:2", 1)
		reviewedSpec, nextSpec := source.spec(t, namespace), next.spec(t, namespace)
		changed, err := unapprovedElevatedServices(deployment.Plan{ID: uuid.New(), ServerID: server, Specification: reviewedSpec}, deployment.Plan{ID: uuid.New(), ServerID: server, Specification: nextSpec})
		if err != nil || len(changed) != 0 {
			t.Fatalf("a port-only redeploy needed review: %v", err)
		}
		if _, err = automaticChanges(reviewedSpec, nextSpec); err != nil {
			t.Fatalf("a port-only redeploy was refused automatically: %v", err)
		}
	})
}
