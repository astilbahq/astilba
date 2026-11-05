package txnetwork_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/txnetwork"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), txnetwork.Analyzer, "github.com/astilbahq/astilba/services/platform/internal/admit")
}
