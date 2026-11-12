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
	Name:      "secretkeys",
	Doc:       "reports use of the keyring or a custody backend outside the secret broker",
	Run:       run,
	FactTypes: []analysis.Fact{new(transitional)},
}

// transitional marks a function in a pending file that reaches keys, so that a call to it from
// another package is reported too.
type transitional struct{}

func (*transitional) AFact()         {}
func (*transitional) String() string { return "transitional" }

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

// legacyCallers are the files that reach, through any chain of calls, a function in a pending
// file that uses keys. Those paths open values without a record, so a call into one from
// anywhere else is reported: the files below move to the broker with them (mostly Git access
// through gitAnswers, and the handlers that reach it), and none may be added.
var legacyCallers = map[string]bool{
	"internal/execution/automatic.go":             true,
	"internal/execution/automation.go":            true,
	"internal/execution/backups.go":               true,
	"internal/execution/cross_host_operations.go": true,
	"internal/execution/cross_host_plans.go":      true,
	"internal/execution/git_answers.go":           true,
	"internal/execution/git_refresh.go":           true,
	"internal/execution/github_app.go":            true,
	"internal/execution/maintenance.go":           true,
	"internal/execution/operations.go":            true,
	"internal/execution/pipelines.go":             true,
	"internal/execution/preview_cleanup.go":       true,
	"internal/execution/preview_workers.go":       true,
	"internal/execution/previews.go":              true,
	"internal/execution/source_state.go":          true,
	"internal/execution/transport.go":             true,
	"internal/execution/upgrades.go":              true,
	"internal/execution/visual.go":                true,
	"internal/httpapi/automation.go":              true,
	"internal/httpapi/execution.go":               true,
	"internal/httpapi/github_app.go":              true,
	"internal/httpapi/pipelines.go":               true,
	"internal/httpapi/previews.go":                true,
	"internal/httpapi/upgrades.go":                true,
	"internal/httpapi/visual.go":                  true,
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
	pkg := strings.TrimSuffix(trimmed(pass.Pkg.Path()), "_test")
	if keyPackages[pkg] {
		return nil, nil
	}
	report := func(pos token.Pos, what string) {
		pass.Reportf(pos, "%s gives keys outside the secret broker; ask secretbroker for a session instead", what)
	}
	legacy := map[types.Object]bool{}
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
	// Functions that reach keys open values without a record: those in pending files that use
	// keys, and, until nothing changes, every function in a pending or listed caller file that
	// calls one. Each is recorded as a fact, so a caller in another package is held to the same
	// list.
	isLegacy := func(object types.Object) bool {
		switch v := object.(type) {
		case *types.Func:
			object = v.Origin()
		case *types.Var:
			// A package-level variable can hold a transitional function.
			if v.Parent() != v.Pkg().Scope() {
				return false
			}
		default:
			return false
		}
		return legacy[object] || object.Pkg() != pass.Pkg && pass.ImportObjectFact(object, new(transitional))
	}
	// Each function, and each package-level variable's initialiser, in a pending or listed file.
	type function struct {
		object types.Object
		body   ast.Node
		file   string
	}
	var functions []function
	for _, file := range pass.Files {
		name := pkg + "/" + path.Base(pass.Fset.Position(file.Pos()).Filename)
		if !pending[name] && !legacyCallers[name] {
			continue
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Body != nil {
					functions = append(functions, function{pass.TypesInfo.Defs[decl.Name], decl.Body, name})
				}
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, id := range value.Names {
						if i < len(value.Values) {
							functions = append(functions, function{pass.TypesInfo.Defs[id], value.Values[i], name})
						}
					}
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range functions {
			if legacy[fn.object] {
				continue
			}
			ast.Inspect(fn.body, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && !legacy[fn.object] && (pending[fn.file] && uses(id) != "" || isLegacy(pass.TypesInfo.Uses[id])) {
					legacy[fn.object] = true
					changed = true
				}
				return true
			})
		}
	}
	for _, fn := range functions {
		if legacy[fn.object] {
			pass.ExportObjectFact(fn.object, new(transitional))
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
			} else if object := pass.TypesInfo.Uses[id]; isLegacy(object) && !legacyCallers[name] {
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
