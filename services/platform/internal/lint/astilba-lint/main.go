// astilba-lint runs the repository's own analyzers; see docs/continuous-integration.md.
package main

import (
	"github.com/astilbahq/astilba/services/platform/internal/lint/dberror"
	"github.com/astilbahq/astilba/services/platform/internal/lint/jsonguard"
	"github.com/astilbahq/astilba/services/platform/internal/lint/jsontag"
	"github.com/astilbahq/astilba/services/platform/internal/lint/operationview"
	"github.com/astilbahq/astilba/services/platform/internal/lint/secretkeys"
	"github.com/astilbahq/astilba/services/platform/internal/lint/txnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/lint/wsclose"
	"golang.org/x/tools/go/analysis/multichecker"
)

func main() {
	multichecker.Main(dberror.Analyzer, jsonguard.Analyzer, jsontag.Analyzer, operationview.Analyzer, secretkeys.Analyzer, txnetwork.Analyzer, wsclose.Analyzer)
}
