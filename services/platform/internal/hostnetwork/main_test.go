package hostnetwork

import (
	"os"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lanebudget"
)

// A run whose package goes past its lane's time budget warns in the pull request's checks.
func TestMain(m *testing.M) {
	done := lanebudget.Start()
	code := m.Run()
	done()
	os.Exit(code)
}
