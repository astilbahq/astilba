package domain

import (
	"encoding/json"
	"github.com/google/uuid"
	"testing"
)

func TestCrossHostPlacementPreservesStatefulOwnership(t *testing.T) {
	image := "example.test/app:1"
	app, primary, remote := uuid.New(), uuid.New(), uuid.New()
	spec := Specification{Version: 1, Variables: []Variable{}, Applications: []Application{{ID: app, Name: "App", Source: ApplicationSource{Kind: "image", Image: &image}, Replicas: 3, Health: &HealthCheck{Port: 8080, Path: "/ready"}}}, Routes: []Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "app.example.test", Port: 8080, TLS: "off"}}, Placement: &Placement{NetworkID: uuid.New(), IngressServerID: primary, Applications: []ApplicationPlacement{{ApplicationID: app, CPUMillis: 500, MemoryMiB: 256, Targets: []PlacementTarget{{ServerID: primary, Count: 1}, {ServerID: remote, Count: 2}}}}}}
	parse := func(s Specification) error { b, _ := json.Marshal(s); _, _, err := ParseSpec(b); return err }
	if err := parse(spec); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"count", "mounted-singleton", "duplicate-host", "unknown-application"} {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(spec)
			var next Specification
			_ = json.Unmarshal(b, &next)
			switch name {
			case "count":
				next.Placement.Applications[0].Targets[1].Count = 1
			case "mounted-singleton":
				next.Applications[0].Replicas = 1
				next.Placement.Applications[0].Targets = []PlacementTarget{{ServerID: remote, Count: 1}}
				vol := uuid.New()
				next.Volumes = []Volume{{ID: vol, Name: "Files"}}
				next.Applications[0].Mounts = []VolumeMount{{VolumeID: vol, Target: "/data"}}
			case "duplicate-host":
				next.Placement.Applications[0].Targets[1].ServerID = primary
			case "unknown-application":
				next.Placement.Applications[0].ApplicationID = uuid.New()
			}
			if parse(next) == nil {
				t.Fatal("unsupported distributed configuration accepted")
			}
		})
	}
}
