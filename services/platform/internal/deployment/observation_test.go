package deployment

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestObservationRejectsAmbiguousInventoryAndUnboundedState(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, change := range map[string]func(*Observation){
		"duplicate resource": func(o *Observation) { o.Resources = append(o.Resources, o.Resources[0]) },
		"nil instance":       func(o *Observation) { o.Resources[0].Instances[0].InstanceID = uuid.Nil },
		"duplicate instance": func(o *Observation) {
			o.Resources[0].Instances = append(o.Resources[0].Instances, o.Resources[0].Instances[0])
		},
		"resource state": func(o *Observation) { o.Resources[0].State = strings.Repeat("x", 31) },
		"instance state": func(o *Observation) { o.Resources[0].Instances[0].State = strings.Repeat("x", 31) },
		"missing time":   func(o *Observation) { o.ObservedAt = time.Time{} },
		"future time":    func(o *Observation) { o.ObservedAt = now.Add(2 * time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			o := Observation{ObservedAt: now, Resources: []Resource{{ID: uuid.New(), State: "running", Instances: []InstanceState{{InstanceID: uuid.New(), State: "running"}}}}}
			if err := o.Validate(now); err != nil {
				t.Fatal("valid inventory rejected", err)
			}
			change(&o)
			if err := o.Validate(now); err == nil {
				t.Fatal("malformed inventory accepted")
			}
		})
	}
}
