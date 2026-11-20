// Package txnetwork reports a call to a Git provider, GitHub, Stripe or the DNS provider, or a
// request to the secret broker for keys, made with a transaction or a pinned pool connection in
// hand, where a slow provider or the key service would hold that transaction's locks and
// connection: a tenant's slow Git server, or Stripe or the DNS provider, which every tenant
// shares. The broker also commits a record of its own before it gives keys out, which a
// caller's transaction must not contain.
package txnetwork

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

var Analyzer = &analysis.Analyzer{
	Name: "txnetwork",
	Doc:  "reports a Git provider, GitHub, Stripe or DNS provider call, or a secret broker session begun, directly or through a function in the same package, from a function holding a transaction or pinned pool connection",
	Run:  run,
}

const (
	txType       = "github.com/jackc/pgx/v5.Tx"
	connType     = "github.com/jackc/pgx/v5/pgxpool.Conn"
	queriesType  = "github.com/astilbahq/astilba/services/platform/db/generated.Queries"
	providerType = "github.com/astilbahq/astilba/services/platform/internal/pipeline.Provider"
	clientType   = "github.com/astilbahq/astilba/services/platform/internal/githubapp.Client"
	brokerType   = "github.com/astilbahq/astilba/services/platform/internal/secretbroker.Broker"
	storeType    = "github.com/astilbahq/astilba/services/platform/internal/store.Store"
	stripeType   = "github.com/astilbahq/astilba/services/platform/internal/hosting.Stripe"
	dnsType      = "github.com/astilbahq/astilba/services/platform/internal/applicationdns.Cloudflare"
)

// brokerMethods are the Broker methods that record and give out keys, and storeKeyMethods the
// Store methods that admit a caller in a transaction of their own and then do the same.
var (
	brokerMethods   = map[string]bool{"Begin": true, "BeginRotation": true, "Tokens": true, "Create": true, "InitKeys": true}
	storeKeyMethods = map[string]bool{"Admit": true, "BeginKeys": true}
)

// offlineClientMethods are the Client methods that only compute or check a value locally, and
// offlineProviderMethods the Provider methods that only set one.
var (
	offlineClientMethods   = map[string]bool{"FixtureAPI": true, "InstallURL": true, "AuthorizeURL": true, "VerifyWebhook": true, "Forget": true}
	offlineProviderMethods = map[string]bool{"OnRejected": true}
)

type function struct {
	signature *ast.FuncType
	body      *ast.BlockStmt
}

func run(pass *analysis.Pass) (any, error) {
	// A function literal is its own body: one that never mentions the transaction, and isn't
	// called on the spot, is a call to make after it, as the providerCall closures are.
	functions := []function{}
	declared := map[types.Object]*ast.BlockStmt{}
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch f := n.(type) {
			case *ast.FuncDecl:
				if f.Body != nil {
					functions = append(functions, function{f.Type, f.Body})
					declared[pass.TypesInfo.Defs[f.Name]] = f.Body
				}
			case *ast.FuncLit:
				functions = append(functions, function{f.Type, f.Body})
			}
			return true
		})
	}
	reaches := map[*ast.BlockStmt]bool{}
	contacts := func(call *ast.CallExpr) bool {
		callee := typeutil.Callee(pass.TypesInfo, call)
		if fn, ok := callee.(*types.Func); ok {
			callee = fn.Origin()
		}
		return networkMethod(callee) || reaches[declared[callee]]
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range functions {
			inspectOwn(fn.body, func(n ast.Node) {
				if call, ok := n.(*ast.CallExpr); ok && !reaches[fn.body] && contacts(call) {
					reaches[fn.body] = true
					changed = true
				}
			})
		}
	}
	// A literal called on the spot is visited for its own body as well as its caller's.
	reported := map[*ast.CallExpr]bool{}
	for _, fn := range functions {
		held := transactionFrom(pass, fn)
		if held == token.NoPos {
			continue
		}
		inspectOwn(fn.body, func(n ast.Node) {
			if call, ok := n.(*ast.CallExpr); ok && call.Pos() > held && !reported[call] && contacts(call) {
				reported[call] = true
				pass.Reportf(call.Pos(), "this contacts a Git provider, GitHub, Stripe, the DNS provider or the secret broker with a transaction or pinned connection in hand, holding its locks and connection; ask between passes with nothing held, or begin keys before the transaction")
			}
		})
	}
	return nil, nil
}

// inspectOwn visits a body, and any literal it calls on the spot or defers, without entering
// the other function literals inside it. A goroutine holds nothing of its caller's, though
// the caller evaluates its arguments.
func inspectOwn(body *ast.BlockStmt, visit func(ast.Node)) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch f := n.(type) {
		case *ast.CallExpr:
			if lit, ok := ast.Unparen(f.Fun).(*ast.FuncLit); ok {
				inspectOwn(lit.Body, visit)
			}
		case *ast.FuncLit:
			return false
		case *ast.GoStmt:
			for _, argument := range f.Call.Args {
				inspectOwn(&ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: argument}}}, visit)
			}
			return false
		}
		visit(n)
		return true
	})
}

// transactionFrom finds where a function first has a transaction or a pinned pool connection:
// from its start if it is given one or captures one, and otherwise once it obtains one, so that
// a call made before opening a transaction, as the GitHub reconcile worker does, is correct. A
// connection counts until the function returns, released or not. A literal naming an outer
// transaction, connection or queries has them from its first line. Queries otherwise count
// only as a parameter, because a local may be pool-backed, and a field holds nothing here: the
// store's Queries are pool-backed, and a struct holding a transaction is not looked into.
func transactionFrom(pass *analysis.Pass, fn function) token.Pos {
	for _, parameter := range fn.signature.Params.List {
		if holds(pass.TypesInfo.TypeOf(parameter.Type)) || queries(pass.TypesInfo.TypeOf(parameter.Type)) {
			return fn.body.Lbrace
		}
	}
	from := token.NoPos
	assigned := map[*ast.Ident]bool{}
	note := func(pos token.Pos) {
		if from == token.NoPos || pos < from {
			from = pos
		}
	}
	ast.Inspect(fn.body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit, *ast.FuncType:
			return false
		case *ast.ValueSpec:
			if len(node.Values) == 0 {
				return false
			}
			for _, name := range node.Names {
				if isTransaction(pass, name) {
					assigned[name] = true
					note(node.End())
				}
			}
		case *ast.AssignStmt:
			for _, target := range node.Lhs {
				if id, ok := target.(*ast.Ident); ok && isTransaction(pass, id) {
					assigned[id] = true
					note(node.End())
				}
			}
		case *ast.Ident:
			if captured(pass, node, fn.signature.Pos()) {
				note(fn.body.Lbrace)
			} else if !assigned[node] && isTransaction(pass, node) {
				note(node.Pos())
			}
		}
		return true
	})
	return from
}

// captured reports an outer transaction, connection or queries named from inside a literal.
func captured(pass *analysis.Pass, id *ast.Ident, signature token.Pos) bool {
	v, ok := pass.TypesInfo.ObjectOf(id).(*types.Var)
	return ok && !v.IsField() && v.Pos() < signature && v.Parent() != pass.Pkg.Scope() && (holds(v.Type()) || queries(v.Type()))
}

func isTransaction(pass *analysis.Pass, id *ast.Ident) bool {
	v, ok := pass.TypesInfo.ObjectOf(id).(*types.Var)
	return ok && !v.IsField() && holds(v.Type())
}

// holds reports a transaction, or a connection taken out of the pool.
func holds(t types.Type) bool {
	return typeName(t) == txType || pointerTo(t) == connType
}

func queries(t types.Type) bool {
	return pointerTo(t) == queriesType
}

func pointerTo(t types.Type) string {
	pointer, ok := types.Unalias(t).(*types.Pointer)
	if !ok {
		return ""
	}
	return typeName(pointer.Elem())
}

func typeName(t types.Type) string {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return ""
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name()
}

func networkMethod(callee types.Object) bool {
	fn, ok := callee.(*types.Func)
	if !ok {
		return false
	}
	receiver := fn.Type().(*types.Signature).Recv()
	if receiver == nil {
		return false
	}
	named := typeName(receiver.Type())
	if named == "" {
		named = pointerTo(receiver.Type())
	}
	switch named {
	case stripeType, dnsType:
		// Every method of the Stripe and DNS clients asks the provider.
		return true
	case providerType:
		return !offlineProviderMethods[fn.Name()]
	case clientType:
		return !offlineClientMethods[fn.Name()]
	case brokerType:
		return brokerMethods[fn.Name()]
	case storeType:
		return storeKeyMethods[fn.Name()]
	}
	return false
}
