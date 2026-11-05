package integration

import (
	"testing"
)

func TestCrossHostSubscriptionExpiry(t *testing.T) {
	for _, phase := range []string{"probe", "publish"} {
		t.Run(phase, func(t *testing.T) {
			testCrossHostCoordinatedPlacement(t, phase)
		})
	}
}
