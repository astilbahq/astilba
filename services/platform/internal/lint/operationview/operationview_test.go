package operationview_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/operationview"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), operationview.Analyzer, "example/internal/execution", "example/internal/httpapi")
}
