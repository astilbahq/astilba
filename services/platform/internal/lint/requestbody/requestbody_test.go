package requestbody_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/requestbody"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), requestbody.Analyzer, "example/internal/httpapi", "example/internal/other")
}
