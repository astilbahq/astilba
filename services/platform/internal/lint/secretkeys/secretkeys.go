// Package secretkeys keeps the keys to stored values inside the secret broker: outside it and
// start-up wiring, no management code may hold or use the installation keyring or a custody
// backend, so every open goes through the broker's recorded path.
package secretkeys

import (
	"go/ast"
	"go/types"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "secretkeys",
	Doc:  "reports use of the keyring or a custody backend outside the secret broker",
	Run:  run,
}

const (
	cryptoboxPath = "internal/cryptobox"
	custodyPath   = "internal/custody"
)

// keyPackages may hold keys: the broker and the backends it uses, start-up wiring, and the
// agent, whose journal has a keyring of its own on its host.
var keyPackages = map[string]bool{
	"internal/cryptobox":    true,
	"internal/custody":      true,
	"internal/secretbroker": true,
	// A broker for unit tests, with its records in memory.
	"internal/secretbroker/brokertest": true,
	"cmd/server":                       true,
	"internal/agent":                   true,
}

// harmless are the cryptobox names that hold no key.
var harmless = map[string]bool{"ErrKey": true, "ErrTooLarge": true}

// modules are the module paths packages are named under; package paths below are relative to
// them, so that a nested package such as cmd/x/internal/agent is not mistaken for internal/agent.
var modules = []string{"github.com/astilbahq/astilba/services/platform/", "example.com/platform/"}

// trimmed returns a package path relative to its module.
func trimmed(pkg string) string {
	for _, module := range modules {
		if rest, ok := strings.CutPrefix(pkg, module); ok {
			return rest
		}
	}
	return pkg
}

func run(pass *analysis.Pass) (any, error) {
	if keyPackages[strings.TrimSuffix(trimmed(pass.Pkg.Path()), "_test")] {
		return nil, nil
	}
	report := func(n ast.Node, what string) {
		pass.Reportf(n.Pos(), "%s gives keys outside the secret broker; ask secretbroker for a session instead", what)
	}
	for _, file := range pass.Files {
		for _, spec := range file.Imports {
			if value, _ := strconv.Unquote(spec.Path.Value); trimmed(value) == custodyPath {
				report(spec, "the custody package")
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if why := holdsKeys(pass.TypesInfo.Uses[id]); why != "" {
				report(id, why)
			} else if keyring(pass.TypesInfo.TypeOf(id)) {
				// A field or variable holding the keyring.
				report(id, id.Name+", a keyring,")
			}
			return true
		})
	}
	return nil, nil
}

func keyring(t types.Type) bool {
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() != nil && trimmed(named.Obj().Pkg().Path()) == cryptoboxPath && named.Obj().Name() == "Keyring"
}

// holdsKeys names what an identifier refers to when it is a way to keys, or returns "".
func holdsKeys(object types.Object) string {
	if object == nil || object.Pkg() == nil {
		return ""
	}
	if _, ok := object.(*types.PkgName); ok {
		return ""
	}
	switch trimmed(object.Pkg().Path()) {
	case custodyPath:
		return "custody." + object.Name()
	case cryptoboxPath:
		if harmless[object.Name()] {
			return ""
		}
		return "cryptobox." + object.Name()
	}
	return ""
}
