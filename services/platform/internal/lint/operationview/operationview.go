// Package operationview reports an operation given to a principal other than through package
// execution's operationFor or operationsFor, which withhold its note and step details from a
// principal that may not read the environment.
package operationview

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

var Analyzer = &analysis.Analyzer{
	Name: "operationview",
	Doc:  "reports a function that takes a store.Principal and returns an api.Operation, or a slice of them, other than through execution's operationFor or operationsFor",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	execution := strings.HasSuffix(pass.Pkg.Path(), "/internal/execution")
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			view, principals := viewFor(pass, fn.Type)
			if view == "" {
				continue
			}
			if !execution {
				pass.Reportf(fn.Name.Pos(), "an operation reaches a principal only through package execution, whose %s withholds what it may not read", view)
				continue
			}
			// The package's exported functions are what reach a principal. An unexported one that takes
			// a principal works for one of them, which shows the principal what it returns.
			if !fn.Name.IsExported() {
				continue
			}
			inspectOwn(fn.Body, func(ret *ast.ReturnStmt) {
				if len(ret.Results) == 0 || !empty(pass, ret.Results[0]) && !calls(pass, ret.Results[0], view, principals) {
					pass.Reportf(ret.Pos(), "return the operation through %s with the function's own principal, which withholds what it may not read", view)
				}
			})
		}
	}
	return nil, nil
}

// viewFor names the function a principal-facing function must return its operations through, with
// its principal parameters, or is empty when the function takes no principal or returns no
// operations first.
func viewFor(pass *analysis.Pass, signature *ast.FuncType) (string, map[types.Object]bool) {
	if signature.Results == nil || len(signature.Results.List) == 0 {
		return "", nil
	}
	principals := map[types.Object]bool{}
	for _, field := range signature.Params.List {
		if named(pass.TypesInfo.TypeOf(field.Type), "/internal/store", "Principal") {
			for _, name := range field.Names {
				principals[pass.TypesInfo.Defs[name]] = true
			}
		}
	}
	if len(principals) == 0 {
		return "", nil
	}
	first := pass.TypesInfo.TypeOf(signature.Results.List[0].Type)
	if named(first, "/internal/api", "Operation") {
		return "operationFor", principals
	}
	if slice, ok := first.(*types.Slice); ok && named(slice.Elem(), "/internal/api", "Operation") {
		return "operationsFor", principals
	}
	return "", nil
}

func named(t types.Type, pkg, name string) bool {
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	n, ok := t.(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return false
	}
	return n.Obj().Name() == name && strings.HasSuffix(n.Obj().Pkg().Path(), pkg)
}

// empty is whether a result is nil or a literal with no elements, which carries nothing to withhold.
func empty(pass *analysis.Pass, expr ast.Expr) bool {
	expr = ast.Unparen(expr)
	if literal, ok := expr.(*ast.CompositeLit); ok {
		return len(literal.Elts) == 0
	}
	return pass.TypesInfo.Types[expr].IsNil()
}

// calls is whether expr calls view with one of the function's own principals, so the operation is
// shown as the caller may see it and not as some other principal would.
func calls(pass *analysis.Pass, expr ast.Expr, view string, principals map[types.Object]bool) bool {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	fn, ok := typeutil.Callee(pass.TypesInfo, call).(*types.Func)
	if !ok || fn.Name() != view || fn.Pkg() != pass.Pkg {
		return false
	}
	arg := ast.Unparen(call.Args[0])
	if star, ok := arg.(*ast.StarExpr); ok {
		arg = ast.Unparen(star.X)
	}
	id, ok := arg.(*ast.Ident)
	return ok && principals[pass.TypesInfo.Uses[id]]
}

// inspectOwn visits the function's own return statements, not those of the literals inside it.
func inspectOwn(body *ast.BlockStmt, visit func(*ast.ReturnStmt)) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			visit(n)
		}
		return true
	})
}
