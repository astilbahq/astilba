package execution

import (
	"maps"
	"slices"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
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

func TestElevatedServiceIDsNamePrivilegedServicesAndTheirPeers(t *testing.T) {
	for name, test := range map[string]struct {
		compose string
		want    []string
	}{
		"volume and network peers": {peerCompose, []string{"client", "dind", "runner"}},
		"default network":          {defaultCompose, []string{"tuner", "worker"}},
	} {
		t.Run(name, func(t *testing.T) {
			namespace := uuid.New()
			spec := reviewSource{compose: test.compose}.spec(t, namespace)
			elevated, err := ElevatedServiceIDs(spec)
			if err != nil {
				t.Fatal(err)
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
	visual, err := ElevatedServiceIDs(domain.Specification{Version: 1, Applications: []domain.Application{{ID: uuid.New(), Name: "web", Kind: "web"}}})
	if err != nil || len(visual) != 0 {
		t.Fatalf("a visual specification has elevated services: %v %v", slices.Collect(maps.Keys(visual)), err)
	}
}
