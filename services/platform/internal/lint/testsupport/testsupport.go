// Package testsupport keeps test-support packages out of the product: only test files, and the
// test-support packages themselves, may import them. They answer as fakes, and the lints that
// exempt them, such as requestbody and jsonguard, do so because nothing else runs their code.
package testsupport

import (
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "testsupport",
	Doc:  "reports a test-support package imported outside test files",
	Run:  run,
}

// packages are the test-support packages, named relative to the module.
var packages = map[string]bool{
	"internal/pipeline/pipelinetest":   true,
	"internal/secretbroker/brokertest": true,
}

// modules are the module paths packages are named under, so that a nested package such as
// cmd/x/internal/pipeline/pipelinetest is not mistaken for one of them.
var modules = []string{"github.com/astilbahq/astilba/services/platform/", "example.com/platform/"}

func support(pkg string) bool {
	for _, module := range modules {
		if rest, ok := strings.CutPrefix(pkg, module); ok {
			return packages[rest]
		}
	}
	return false
}

func run(pass *analysis.Pass) (any, error) {
	if support(strings.TrimSuffix(pass.Pkg.Path(), "_test")) {
		return nil, nil
	}
	for _, file := range pass.Files {
		if strings.HasSuffix(pass.Fset.Position(file.Pos()).Filename, "_test.go") {
			continue
		}
		for _, spec := range file.Imports {
			if value, _ := strconv.Unquote(spec.Path.Value); support(value) {
				pass.Reportf(spec.Pos(), "%s is test support; import it only from test files", value)
			}
		}
	}
	return nil, nil
}
