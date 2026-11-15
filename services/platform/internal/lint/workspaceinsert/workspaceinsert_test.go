package workspaceinsert_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/workspaceinsert"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), workspaceinsert.Analyzer, "example/internal/auth", "example/internal/store")
}
