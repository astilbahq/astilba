package testsupport_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/lint/testsupport"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), testsupport.Analyzer,
		"example.com/platform/internal/execution", "example.com/platform/cmd/tool", "example.com/platform/internal/pipeline/pipelinetest")
}
