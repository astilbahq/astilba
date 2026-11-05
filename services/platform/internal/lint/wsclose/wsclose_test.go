package wsclose_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/wsclose"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), wsclose.Analyzer, "relay", "example/internal/terminal")
}
