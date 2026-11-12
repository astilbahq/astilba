// Package secretkeys keeps the keys to stored values inside the secret broker: outside it and
// start-up wiring, no management code may hold or use the installation keyring or a custody
// backend, so every open goes through the broker's recorded path.
package secretkeys

import (
	"go/ast"
	"go/token"
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

// pending are the files that still hold keys without the broker, as package/file. None may be
// added; each goes when its path moves to the broker: Git access in multi-pass work, GitHub
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

// legacyCallers are the files that call functions in pending files which use keys. Those
// functions open values without a record, so a call from anywhere else is reported: the files
// below move to the broker with them, and none may be added.
var legacyCallers = map[string]bool{
	"internal/execution/backups.go":               true,
	"internal/execution/cross_host_operations.go": true,
	"internal/execution/cross_host_plans.go":      true,
	"internal/execution/git_answers.go":           true,
	"internal/execution/github_app.go":            true,
	"internal/execution/maintenance.go":           true,
	"internal/execution/pipelines.go":             true,
	"internal/execution/preview_workers.go":       true,
	"internal/execution/previews.go":              true,
	"internal/execution/transport.go":             true,
	"internal/execution/upgrades.go":              true,
	"internal/execution/visual.go":                true,
}

// harmless are the cryptobox names that hold no key.
var harmless = map[string]bool{"ErrKey": true, "ErrTooLarge": true}

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
	report := func(pos token.Pos, what string) {
		pass.Reportf(pos, "%s gives keys outside the secret broker; ask secretbroker for a session instead", what)
	}
	uses := func(id *ast.Ident) string {
		if why := holdsKeys(pass.TypesInfo.Uses[id]); why != "" {
			return why
		}
		if keyring(pass.TypesInfo.TypeOf(id)) {
			// A field or variable holding the keyring, such as a store's.
			return id.Name + ", a keyring,"
		}
		return ""
	}
	// Functions in pending files that use keys open values without a record.
	legacy := map[types.Object]bool{}
	for _, file := range pass.Files {
		if !pending[pkg+"/"+path.Base(pass.Fset.Position(file.Pos()).Filename)] {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && uses(id) != "" {
					legacy[pass.TypesInfo.Defs[fn.Name]] = true
				}
				return true
			})
		}
	}
	for _, file := range pass.Files {
		name := pkg + "/" + path.Base(pass.Fset.Position(file.Pos()).Filename)
		if pending[name] {
			continue
		}
		for _, spec := range file.Imports {
			if value, _ := strconv.Unquote(spec.Path.Value); trimmed(value) == custodyPath {
				report(spec.Pos(), "the custody package")
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if why := uses(id); why != "" {
				report(id.Pos(), why)
			} else if object := pass.TypesInfo.Uses[id]; object != nil && legacy[object] && !legacyCallers[name] {
				report(id.Pos(), object.Name()+", which opens values without a record,")
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
