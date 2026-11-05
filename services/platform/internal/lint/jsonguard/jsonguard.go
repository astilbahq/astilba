// Package jsonguard keeps JSON encoding decisions in one place: encoding/json only
// where docs/json-encoding.md gives a reason, and plain encoding/json/v2 output only
// where nothing hashes, stores or compares it.
package jsonguard

import (
	"go/ast"
	"go/types"
	"path"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "jsonguard",
	Doc:  "reports encoding/json imports and plain encoding/json/v2 writers outside the reasons in docs/json-encoding.md",
	Run:  run,
}

// Files with a recorded reason to name encoding/json, as package/file.
var v1Importers = map[string]bool{
	"internal/stablejson/golden_test.go": true, // decodes with v1, as an older peer does
	"internal/stablejson/stablejson.go":  true,
	"internal/domain/compose.go":         true,
	"internal/cryptobox/cryptobox.go":    true,
	"internal/pipeline/railpack_plan.go": true,
}

// Functions that write to a third party, as package.receiver.function. Nothing hashes,
// stores or compares what they send, so they do not need the frozen format.
var plainWriters = map[string]bool{
	"internal/stablejson.Marshal":                true,
	"internal/pipeline.Provider.request":         true,
	"internal/applicationdns.Cloudflare.request": true,
	"internal/githubapp.Client.jwt":              true,
	"internal/githubapp.Client.request":          true,
	"internal/githubapp.Client.RevokeUser":       true,
	"internal/alerts.Service.send":               true,
	"internal/agent.visualRegistryAuth":          true,
	"internal/agent.buildKitAuthConfig":          true,
}

func run(pass *analysis.Pass) (any, error) {
	pkg := pass.Pkg.Path()
	if i := strings.Index(pkg, "internal/"); i >= 0 {
		pkg = pkg[i:]
	}
	pkg = strings.TrimSuffix(pkg, "_test")
	for _, file := range pass.Files {
		name := path.Base(pass.Fset.Position(file.Pos()).Filename)
		for _, spec := range file.Imports {
			if value, _ := strconv.Unquote(spec.Path.Value); value == "encoding/json" && !v1Importers[pkg+"/"+name] {
				pass.Reportf(spec.Pos(), "encoding/json folds case and accepts duplicates; read with encoding/json/v2 and write with stablejson, or record the reason in docs/json-encoding.md")
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				return !plainWriters[pkg+"."+funcName(n)]
			case *ast.Ident:
				if called, ok := pass.TypesInfo.Uses[n].(*types.Func); ok && called.Pkg() != nil && writes(called) {
					pass.Reportf(n.Pos(), "plain encoding/json/v2 output moves digests when the value is read back; use stablejson, or add this third-party writer to jsonguard")
				}
			}
			return true
		})
	}
	return nil, nil
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	receiver := fn.Recv.List[0].Type
	if star, ok := receiver.(*ast.StarExpr); ok {
		receiver = star.X
	}
	switch generic := receiver.(type) {
	case *ast.IndexExpr:
		receiver = generic.X
	case *ast.IndexListExpr:
		receiver = generic.X
	}
	if ident, ok := receiver.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return "?." + fn.Name.Name
}

func writes(fn *types.Func) bool {
	switch fn.Pkg().Path() {
	case "encoding/json/v2":
		return fn.Name() == "Marshal" || fn.Name() == "MarshalWrite" || fn.Name() == "MarshalEncode"
	case "encoding/json/jsontext":
		return fn.Name() == "NewEncoder"
	}
	return false
}
