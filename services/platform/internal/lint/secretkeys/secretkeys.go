// Package secretkeys keeps the keys to stored values inside the secret broker: outside it and
// start-up wiring, no management code may hold or use the installation keyring or a custody
// backend, so every open goes through the broker's recorded path.
package secretkeys

import (
	"go/ast"
	"go/types"
	"path"
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

// pending are the files that still open values without the broker, as package/file. None may
// be added; each goes when its path moves to the broker: Git access in multi-pass work, GitHub
// grants, previews and recovery checkpoints with "need keys" passes, and the agent delivery
// path with release records (PLAT-243, PLAT-196).
var pending = map[string]bool{
	"internal/store/store.go":                    true,
	"internal/execution/legacy_keys.go":          true,
	"internal/execution/build_inputs.go":         true,
	"internal/execution/cross_host_hydration.go": true,
	"internal/execution/visual_hydration.go":     true,
	"internal/execution/pipeline_report.go":      true,
	"internal/execution/preview_environment.go":  true,
	"internal/execution/recovery_checkpoints.go": true,
}

// trimmed returns a package path from "internal/" or "cmd/" on.
func trimmed(pkg string) string {
	for _, root := range []string{"internal/", "cmd/"} {
		if i := strings.Index(pkg, root); i >= 0 {
			return pkg[i:]
		}
	}
	return pkg
}

func run(pass *analysis.Pass) (any, error) {
	pkg := strings.TrimSuffix(trimmed(pass.Pkg.Path()), "_test")
	if keyPackages[pkg] {
		return nil, nil
	}
	for _, file := range pass.Files {
		name := path.Base(pass.Fset.Position(file.Pos()).Filename)
		if pending[pkg+"/"+name] {
			continue
		}
		for _, spec := range file.Imports {
			if value, _ := strconv.Unquote(spec.Path.Value); trimmed(value) == custodyPath {
				pass.Reportf(spec.Pos(), "the custody package gives keys outside the secret broker; ask secretbroker for a session instead")
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if why := holdsKeys(pass.TypesInfo.Uses[id]); why != "" {
				pass.Reportf(id.Pos(), "%s gives keys outside the secret broker; ask secretbroker for a session instead", why)
			}
			return true
		})
	}
	return nil, nil
}

// holdsKeys names what an identifier refers to when it is a way to keys, or returns "".
func holdsKeys(object types.Object) string {
	if object == nil {
		return ""
	}
	if _, ok := object.(*types.PkgName); ok || object.Pkg() == nil {
		return ""
	}
	switch trimmed(object.Pkg().Path()) {
	case custodyPath:
		return "custody." + object.Name()
	case cryptoboxPath:
	default:
		return ""
	}
	if fn, ok := object.(*types.Func); ok {
		if receiver := fn.Type().(*types.Signature).Recv(); receiver != nil {
			return "Keyring." + fn.Name()
		}
		switch fn.Name() {
		case "New", "Load", "Parse":
			return "cryptobox." + fn.Name()
		}
		return ""
	}
	if _, ok := object.(*types.TypeName); ok && object.Name() == "Keyring" {
		return "cryptobox.Keyring"
	}
	return ""
}
