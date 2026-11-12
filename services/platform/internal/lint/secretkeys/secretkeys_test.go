package secretkeys_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/secretkeys"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), secretkeys.Analyzer,
		"example.com/platform/internal/execution", "example.com/platform/internal/secretbroker", "example.com/platform/internal/httpapi", "example.com/platform/cmd/tool/internal/agent")
}
