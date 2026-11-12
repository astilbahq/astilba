package execution

import (
	"maps"
	"slices"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// peerCompose has a privileged service with a volume peer, a network peer, and a service that
// shares a network only with a peer.
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

// defaultCompose has a service with added capabilities on the default network, beside one
// that names no network and one that names another.
const defaultCompose = `services:
  tuner:
    image: tuner:1
    cap_add: [NET_ADMIN]
  worker:
    image: worker:1
  web:
    image: nginx:1
    networks: [front]
networks:
  front: {}
`

func TestElevationNamesPrivilegedServicesTheirPeersAndWhatTheyReach(t *testing.T) {
	for name, test := range map[string]struct {
		compose string
		want    []string
		reach   func(unit uuid.UUID) []string
	}{
		"volume and network peers": {peerCompose, []string{"client", "dind", "runner"}, func(unit uuid.UUID) []string {
			return []string{deployment.NetworkName(composeproject.ResourceID(unit, "network", "builder")), deployment.VolumeName(composeproject.ResourceID(unit, "volume", "certs"))}
		}},
		"default network": {defaultCompose, []string{"tuner", "worker"}, func(unit uuid.UUID) []string { return []string{deployment.NetworkName(unit)} }},
	} {
		t.Run(name, func(t *testing.T) {
			namespace := uuid.New()
			spec := reviewSource{compose: test.compose}.spec(t, namespace)
			elevation, err := ElevationOf(spec)
			if err != nil {
				t.Fatal(err)
			}
			elevated := elevation.Services
			if got, want := slices.Sorted(maps.Keys(elevation.Reach)), test.reach(namespace); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
				t.Fatalf("reach %v, want %v", got, want)
			}
			names := map[uuid.UUID]string{}
			for _, app := range spec.Applications {
				names[app.ID] = app.Name
			}
			got := []string{}
			for id := range elevated {
				if names[id] == "" {
					t.Fatalf("%s is not a service of the specification", id)
				}
				got = append(got, names[id])
			}
			if slices.Sort(got); !slices.Equal(got, test.want) {
				t.Fatalf("elevated %v, want %v", got, test.want)
			}
			if id := composeproject.ResourceID(namespace, "service", test.want[0]); !elevated[id] {
				t.Fatal("services are not named by their resource ID")
			}
		})
	}
	visual, err := ElevationOf(domain.Specification{Version: 1, Applications: []domain.Application{{ID: uuid.New(), Name: "web", Kind: "web"}}})
	if err != nil || len(visual.Services) != 0 || len(visual.Reach) != 0 {
		t.Fatalf("a visual specification is elevated: %+v %v", visual, err)
	}
}
