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

// legacyFile holds the transitional functions that open and seal integrations without the
// broker. Only pending files and these callers, which move with them, may call them.
const legacyFile = "internal/execution/legacy_keys.go"

var legacyCallers = map[string]bool{
	"internal/execution/backups.go":     true,
	"internal/execution/git_answers.go": true,
	"internal/execution/github_app.go":  true,
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
			object := pass.TypesInfo.Uses[id]
			if why := holdsKeys(object); why != "" {
				report(id.Pos(), why)
			} else if keyring(pass.TypesInfo.TypeOf(id)) {
				// A field or variable holding the keyring, such as a store's.
				report(id.Pos(), id.Name+", a keyring,")
			} else if fn, ok := object.(*types.Func); ok && !legacyCallers[name] && declaredIn(pass, fn) == legacyFile {
				report(id.Pos(), fn.Name()+", which opens values without a record,")
			}
			return true
		})
	}
	return nil, nil
}

// declaredIn is the package/file an object is declared in.
func declaredIn(pass *analysis.Pass, object types.Object) string {
	if object.Pkg() == nil {
		return ""
	}
	return trimmed(object.Pkg().Path()) + "/" + path.Base(pass.Fset.Position(object.Pos()).Filename)
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
