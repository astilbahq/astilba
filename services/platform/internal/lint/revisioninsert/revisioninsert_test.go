package revisioninsert_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/revisioninsert"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), revisioninsert.Analyzer, "example/internal/execution", "example/internal/store")
}
