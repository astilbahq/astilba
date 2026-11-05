package dberror_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/dberror"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), dberror.Analyzer, "example/reads")
}
