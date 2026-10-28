package execution

import (
	"encoding/json"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

func TestUpgradeRecoveryEntitlementIncludesItsParentAuthorization(t *testing.T) {
	parent := uuid.New()
	for _, action := range []string{"prepare", "cutover", "return", "reconcile"} {
		t.Run(action, func(t *testing.T) {
			body := deployment.CrossHostCommand{ParentID: parent, Phase: "effect", EffectAction: action, Maintenance: &deployment.Maintenance{Kind: "upgrade", ResourceID: uuid.New()}}
			raw, _ := json.Marshal(body)
			c := api.ExecutionCommand{Kind: "crosshost", ParentId: &parent, Crosshost: rawMessage(raw)}
			if got, want := recoveryCommand(c), action == "return" || action == "reconcile"; got != want {
				t.Fatalf("upgrade action %s recovery entitlement: %v", action, got)
			}
			c.ParentId = nil
			if recoveryCommand(c) {
				t.Fatal("unparented effect gained a recovery entitlement")
			}
		})
	}
}
