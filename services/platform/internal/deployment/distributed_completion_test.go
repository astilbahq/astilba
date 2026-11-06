package deployment

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// Maintenance reports an application it left stopped; a deployment must have started it.
func TestCompletionRequiresRunningInstancesOnlyFromADeployment(t *testing.T) {
	app, instance := uuid.New(), uuid.New()
	plan := Plan{Images: map[string]string{app.String(): "image"}, Specification: domain.Specification{Applications: []domain.Application{{ID: app}}}, Local: &LocalPlacement{Instances: map[string][]uuid.UUID{app.String(): {instance}}}}
	stopped := Observation{Resources: []Resource{{ID: app, State: "degraded", Image: "image", Instances: []InstanceState{{InstanceID: instance, State: "exited"}}}}}
	missing := Observation{Resources: []Resource{}}
	deploying, maintaining := CrossHostCommand{}, CrossHostCommand{Maintenance: &Maintenance{Kind: "backup"}}
	if deploying.ValidateCompletion(plan, stopped) == nil {
		t.Fatal("a deployment completed with a stopped instance")
	}
	if err := maintaining.ValidateCompletion(plan, stopped); err != nil {
		t.Fatal("maintenance could not report an instance it left stopped: ", err)
	}
	if maintaining.ValidateCompletion(plan, missing) == nil {
		t.Fatal("maintenance completed with an incomplete inventory")
	}
}
