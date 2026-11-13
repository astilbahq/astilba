package deployment

import (
	"encoding/json/v2"
	"os"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func TestLegacySingletonPlanAndComposeRemainCompatible(t *testing.T) {
	// Captured with the preceding compiler at 180ca2138530fe7d6e3eb8dc78c39831c61f7296.
	// Its plan hash and exported Compose bytes must survive omitted extensions.
	raw, err := os.ReadFile("testdata/legacy-singleton.json")
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Plan        Plan   `json:"plan"`
		ComposeHash string `json:"composeHash"`
	}
	if err = json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if err = old.Plan.Validate(true); err != nil {
		t.Fatalf("prior journal plan no longer validates: %v", err)
	}
	compose, err := Compose(old.Plan, nil, true)
	if err != nil || Hash(string(compose)) != old.ComposeHash {
		t.Fatalf("singleton export wire data changed: %v", err)
	}
}

func replicaPlan() Plan {
	p := testPlan()
	web, database := p.Specification.Applications[1].ID, p.Specification.Applications[0].ID
	p.Specification.Routes = []domain.Route{
		{ID: uuid.New(), TargetID: web, Protocol: "http", Domain: "replicas.example.test", Port: 8080, TLS: "off"},
		{ID: uuid.New(), TargetID: database, Protocol: "tcp", Domain: "database.example.test", Port: 5432, ListenPort: 15432, TLS: "off", AllowCIDRs: []string{"127.0.0.1/32"}},
	}
	return p.WithHash()
}

func TestReplicaGroupRenderingAndLegacyRouteRefusal(t *testing.T) {
	old := replicaPlan()
	p := copyPlan(old)
	app := &p.Specification.Applications[1]
	app.Replicas = 3
	p.PrepareInstances(old)
	p.ReplaceInstances(app.ID)
	p = p.WithHash()
	if changed, err := ReleaseApplication(old, p); err != nil || changed.ID != app.ID {
		t.Fatalf("count-only change did not select a stateless release: %v", err)
	}
	secret := p.Specification.Applications[0].Postgres.PasswordSecretVersionID
	raw, err := Compose(p, map[string]string{secret.String(): "example-password"}, false)
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			ContainerName string            `json:"container_name"`
			Labels        map[string]string `json:"labels"`
			Environment   map[string]string `json:"environment"`
		} `json:"services"`
	}
	if json.Unmarshal(raw, &compose) != nil || len(compose.Services) != 4 {
		t.Fatal("replicas were not rendered alongside the singleton database")
	}
	for _, instance := range p.InstanceIDs(app.ID) {
		service := compose.Services[ServiceName(instance)]
		if service.ContainerName != UnitName(instance) || service.Labels["com.astilba.resource"] != app.ID.String() || service.Labels["com.astilba.instance"] != instance.String() || service.Environment["DATABASE_URL"] == "" {
			t.Fatal("replica lost logical ownership, unique identity or its binding")
		}
	}
	set := RouteSet{UnitID: p.UnitID, EnvironmentID: p.EnvironmentID, Routes: p.Specification.Routes, Backends: map[string]Backend{}}
	for _, a := range p.Specification.Applications {
		set.Backends[a.ID.String()] = p.Backend(a.ID)
	}
	raw, err = RouteConfig(map[string]RouteSet{p.UnitID.String(): set})
	if err != nil {
		t.Fatal(err)
	}
	var routes struct {
		HTTP struct {
			Services map[string]struct {
				Balancer struct {
					Servers []struct {
						URL string `json:"url"`
					} `json:"servers"`
					Health *struct {
						Path string `json:"path"`
						Port int    `json:"port"`
					} `json:"healthCheck"`
				} `json:"loadBalancer"`
			} `json:"services"`
		} `json:"http"`
		TCP struct {
			Services map[string]any `json:"services"`
		} `json:"tcp"`
	}
	if json.Unmarshal(raw, &routes) != nil || len(routes.TCP.Services) != 1 || len(routes.HTTP.Services) != 1 {
		t.Fatal("mixed singleton TCP route was lost")
	}
	for _, service := range routes.HTTP.Services {
		if len(service.Balancer.Servers) != 3 || service.Balancer.Health == nil || service.Balancer.Health.Port != app.Health.Port || service.Balancer.Health.Path != app.Health.Path {
			t.Fatal("traffic does not cover the whole pool with declared readiness")
		}
	}
	// The previous consumer knows only Address/InstanceID. Its required-address
	// check must fail rather than republish a primary-only shared host snapshot.
	for key, b := range set.Backends {
		raw, _ := stablejson.Marshal(b)
		var previous struct {
			Address    string     `json:"address"`
			InstanceID *uuid.UUID `json:"instanceId"`
		}
		if json.Unmarshal(raw, &previous) != nil || previous.Address != "" || previous.InstanceID != nil {
			t.Fatal("replica registry became consumable as a legacy singleton")
		}
		set.Backends[key] = Backend{Address: previous.Address, InstanceID: previous.InstanceID}
	}
	if _, err = RouteConfig(map[string]RouteSet{p.UnitID.String(): set}); err == nil {
		t.Fatal("discarded replica metadata published a partial route registry")
	}
	p.ReplicaInstances[app.ID.String()][0] = p.Instance(app.ID)
	p = p.WithHash()
	if p.Validate(true) == nil {
		t.Fatal("two replica identities claimed the same container")
	}
}

func TestReplicaEligibilityPreservesOrdinaryMountedApps(t *testing.T) {
	p := replicaPlan()
	web := &p.Specification.Applications[1]
	volume := uuid.New()
	p.Specification.Volumes = append(p.Specification.Volumes, domain.Volume{ID: volume, Name: "Application state"})
	web.Mounts = []domain.VolumeMount{{VolumeID: volume, Target: "/state"}}
	raw, _ := stablejson.Marshal(p.Specification)
	if _, _, err := domain.ParseSpec(raw); err != nil {
		t.Fatalf("ordinary mounted app fixture is invalid: %v", err)
	}
	web.Replicas = 3
	raw, _ = stablejson.Marshal(p.Specification)
	if _, _, err := domain.ParseSpec(raw); err == nil {
		t.Fatal("replication admitted a persistent writer")
	}
}

// A single-host plan gives an application its own ID as its first instance by leaving it out of
// Instances. One that never left the primary keeps that ID through a placement, so removing the
// placement must leave it out again, and keep the instance a moved application was given.
func TestRemovingPlacementKeepsEachApplicationsInstance(t *testing.T) {
	single := replicaPlan()
	database, web := single.Specification.Applications[0].ID, single.Specification.Applications[1].ID
	moved, movedInstance, replica := uuid.New(), uuid.New(), uuid.New()
	api := single.Specification.Applications[1]
	api.ID, api.Name = moved, "API"
	single.Specification.Applications[1].Replicas = 2
	single.Specification.Applications = append(single.Specification.Applications, api)
	single.Images[moved.String()] = single.Images[web.String()]
	primary := copyPlan(single)
	primary.Recipe = CrossHostRecipe
	primary.Local = &LocalPlacement{Primary: true, Instances: map[string][]uuid.UUID{database.String(): {database}, web.String(): {web, replica}, moved.String(): {movedInstance}}}
	next := copyPlan(single)
	next.ID = uuid.New()
	next.PrepareInstances(primary)
	next = next.WithHash()
	if err := next.Validate(false); err != nil {
		t.Fatalf("the plan without placement is refused: %v", err)
	}
	if next.Instance(database) != database || next.Instance(web) != web || next.Instance(moved) != movedInstance {
		t.Fatal("removing the placement replaced an instance")
	}
	if ids := next.InstanceIDs(web); len(ids) != 2 || ids[1] != replica {
		t.Fatal("removing the placement replaced a replica", ids)
	}
	if _, ok := next.Instances[web.String()]; ok {
		t.Fatal("the default instance was written out, so one identity has two encodings")
	}
}
