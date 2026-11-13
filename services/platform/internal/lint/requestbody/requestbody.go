// Package requestbody reports a request body read other than through httpapi's decode, which
// refuses unknown members and keeps NUL, which PostgreSQL cannot store, out of every body.
package requestbody

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "requestbody",
	Doc:  "reports (*http.Request).Body read outside httpapi's decode and the webhooks that verify a signature over the raw bytes",
	Run:  run,
}

// Handlers that verify a provider's signature over the raw bytes, so they cannot decode first,
// as package.receiver.function. Only the provider can write what they then read, and
// store.Normalize refuses a NUL in it.
var rawReaders = map[string]bool{
	"internal/httpapi.Server.StripeWebhook":    true,
	"internal/httpapi.Server.ReceiveGitHubApp": true,
	"internal/httpapi.Server.ReceiveGitPush":   true,
}

func run(pass *analysis.Pass) (any, error) {
	pkg := pass.Pkg.Path()
	if i := strings.Index(pkg, "internal/"); i >= 0 {
		pkg = pkg[i:]
	}
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := pkg + "." + fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				name = pkg + "." + receiver(fn.Recv.List[0].Type) + "." + fn.Name.Name
			}
			if name == "internal/httpapi.decode" || rawReaders[name] {
				continue
			}
			allowed := map[*ast.SelectorExpr]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if assign, ok := n.(*ast.AssignStmt); ok {
					limit(pass, assign, allowed)
				}
				sel, ok := n.(*ast.SelectorExpr)
				if ok && !allowed[sel] && requestBody(pass, sel) {
					pass.Reportf(sel.Pos(), "a request body read outside decode skips its checks for unknown members and NUL, which PostgreSQL cannot store; read it with decode[T]")
				}
				return true
			})
		}
	}
	return nil, nil
}

// limit allows r.Body = http.MaxBytesReader(w, r.Body, n), which bounds the body decode reads.
func limit(pass *analysis.Pass, assign *ast.AssignStmt, allowed map[*ast.SelectorExpr]bool) {
	if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return
	}
	target, ok := assign.Lhs[0].(*ast.SelectorExpr)
	call, isCall := assign.Rhs[0].(*ast.CallExpr)
	if !ok || !isCall || len(call.Args) != 3 || !requestBody(pass, target) {
		return
	}
	fun, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return
	}
	callee, ok := pass.TypesInfo.Uses[fun.Sel].(*types.Func)
	if !ok || callee.Pkg() == nil || callee.Pkg().Path() != "net/http" || callee.Name() != "MaxBytesReader" {
		return
	}
	if source, ok := call.Args[1].(*ast.SelectorExpr); ok && requestBody(pass, source) {
		allowed[target], allowed[source] = true, true
	}
}

func requestBody(pass *analysis.Pass, sel *ast.SelectorExpr) bool {
	field, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Var)
	if !ok || !field.IsField() || field.Name() != "Body" || field.Pkg() == nil || field.Pkg().Path() != "net/http" {
		return false
	}
	t := pass.TypesInfo.TypeOf(sel.X)
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := t.(*types.Named)
	return ok && named.Obj().Name() == "Request"
}

func receiver(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	if index, ok := e.(*ast.IndexExpr); ok {
		e = index.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
