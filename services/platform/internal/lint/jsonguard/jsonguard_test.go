package jsonguard_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/jsonguard"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), jsonguard.Analyzer, "example/internal/store", "example/internal/stablejson", "example/internal/alerts")
}
