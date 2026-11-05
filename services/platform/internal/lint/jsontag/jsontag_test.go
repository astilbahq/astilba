package jsontag_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/jsontag"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), jsontag.Analyzer, "reads")
}
