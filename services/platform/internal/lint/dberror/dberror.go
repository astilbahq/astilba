// Package dberror reports a failed database read that is treated as a refusal or a missing
// row, so that an outage signs people out, refuses work or reads as absent data.
package dberror

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "dberror",
	Doc:  "reports a branch on a database error that never looks at the error",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			var list []ast.Stmt
			switch n := n.(type) {
			case *ast.BlockStmt:
				list = n.List
			case *ast.CaseClause:
				list = n.Body
			case *ast.CommClause:
				list = n.Body
			}
			for i, stmt := range list {
				var earlier []ast.Expr
				for check, ok := stmt.(*ast.IfStmt); ok; check, ok = check.Else.(*ast.IfStmt) {
					inspectIf(pass, check, earlier, list[:i], list[i+1:])
					earlier = append(earlier, check.Cond)
				}
			}
			return true
		})
	}
	return nil, nil
}

func inspectIf(pass *analysis.Pass, check *ast.IfStmt, earlier []ast.Expr, before, after []ast.Stmt) {
	terms := disjuncts(check.Cond, nil)
terms:
	for _, term := range terms {
		err := comparedWithNil(pass, term)
		if err == nil {
			continue
		}
		for _, condition := range earlier {
			if mentions(pass, err, condition) {
				continue terms
			}
		}
		call := assignedFrom(pass, err, check.Init, before)
		if call == nil || !readCall(pass, call) || used(pass, err, check, terms, term, before, after) {
			continue
		}
		pass.Reportf(term.Pos(), "a failed %s would read as a refusal or a missing row; use store.IfMissing or return the error", callName(call))
	}
}

func disjuncts(e ast.Expr, out []ast.Expr) []ast.Expr {
	e = ast.Unparen(e)
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.LOR {
		return disjuncts(b.Y, disjuncts(b.X, out))
	}
	return append(out, e)
}

// comparedWithNil returns the error variable when e is exactly `err != nil`.
func comparedWithNil(pass *analysis.Pass, e ast.Expr) types.Object {
	b, ok := e.(*ast.BinaryExpr)
	if !ok || b.Op != token.NEQ || !pass.TypesInfo.Types[b.Y].IsNil() {
		return nil
	}
	id, ok := b.X.(*ast.Ident)
	if !ok {
		return nil
	}
	object := pass.TypesInfo.ObjectOf(id)
	if object == nil || !types.Identical(object.Type(), types.Universe.Lookup("error").Type()) {
		return nil
	}
	return object
}

// assignedFrom finds the call whose result the branch is testing: the if's own init, or the
// nearest earlier statement in the same list to assign the variable. A statement in between
// that reads the error has already told its failures apart, as a login does when it returns
// any failure but pgx.ErrNoRows and then branches on the remainder.
func assignedFrom(pass *analysis.Pass, err types.Object, init ast.Stmt, before []ast.Stmt) *ast.CallExpr {
	if call, assigns := assignment(pass, err, init); assigns {
		return call
	}
	for i := len(before) - 1; i >= 0; i-- {
		if call, assigns := assignment(pass, err, before[i]); assigns {
			return call
		}
		if mentions(pass, err, before[i]) {
			return nil
		}
	}
	return nil
}

func assignment(pass *analysis.Pass, err types.Object, stmt ast.Stmt) (*ast.CallExpr, bool) {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok {
		return nil, false
	}
	for i, target := range assign.Lhs {
		id, ok := target.(*ast.Ident)
		if !ok || pass.TypesInfo.ObjectOf(id) != err {
			continue
		}
		value := assign.Rhs[0]
		if len(assign.Rhs) == len(assign.Lhs) {
			value = assign.Rhs[i]
		}
		call, _ := ast.Unparen(value).(*ast.CallExpr)
		return call, true
	}
	return nil, false
}

// readCall matches row scans and generated sqlc methods that return a value. A sqlc :exec
// method or a raw Exec cannot report a missing row, so a branch on its error mistakes nothing.
func readCall(pass *analysis.Pass, call *ast.CallExpr) bool {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	signature := fn.Type().(*types.Signature)
	if signature.Recv() == nil {
		return false
	}
	path := fn.Pkg().Path()
	return strings.HasSuffix(path, "/db/generated") && signature.Results().Len() > 1 || path == "github.com/jackc/pgx/v5" && fn.Name() == "Scan"
}

// used reports whether anything could still tell this failure apart from the branch's other
// outcomes: another condition term, the body or, when the body keeps the error and can fall
// through, whatever runs next. A variable declared outside this block can be read after it by
// code not in view.
func used(pass *analysis.Pass, err types.Object, check *ast.IfStmt, terms []ast.Expr, term ast.Expr, before, after []ast.Stmt) bool {
	for _, other := range terms {
		if other != term && mentions(pass, err, other) {
			return true
		}
	}
	switch firstUse(pass, err, check.Body.List) {
	case read:
		return true
	case replaced:
		return false
	}
	if returns(check.Body) {
		return false
	}
	return firstUse(pass, err, after) == read || declaredOutside(err, before, check)
}

func declaredOutside(err types.Object, before []ast.Stmt, check *ast.IfStmt) bool {
	start := check.Pos()
	if len(before) > 0 {
		start = before[0].Pos()
	}
	return err.Pos() < start
}

type use int

const (
	unused use = iota
	read
	replaced
)

// firstUse reports what statements do to the variable first, so that #147's
// `if e = s.dropStaleSession(ctx); e != nil` does not count as using the failure. A bare
// return reads a named result.
func firstUse(pass *analysis.Pass, err types.Object, stmts []ast.Stmt) use {
	for _, stmt := range stmts {
		first := stmt
		if check, ok := stmt.(*ast.IfStmt); ok && check.Init != nil {
			first = check.Init
		}
		if assign, ok := first.(*ast.AssignStmt); ok {
			if call, assigns := assignment(pass, err, assign); assigns {
				for _, value := range assign.Rhs {
					if mentions(pass, err, value) {
						return read
					}
				}
				// A retry keeps a read's failure in the variable for whatever runs next.
				if call != nil && readCall(pass, call) {
					return unused
				}
				return replaced
			}
		}
		if mentions(pass, err, stmt) || namedResult(err) && bareReturn(stmt) {
			return read
		}
	}
	return unused
}

func mentions(pass *analysis.Pass, err types.Object, n ast.Node) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && pass.TypesInfo.ObjectOf(id) == err {
			found = true
		}
		return !found
	})
	return found
}

func namedResult(err types.Object) bool {
	v, ok := err.(*types.Var)
	return ok && v.Kind() == types.ResultVar
}

func bareReturn(stmt ast.Stmt) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = found || len(n.Results) == 0
		}
		return !found
	})
	return found
}

func returns(body *ast.BlockStmt) bool {
	if len(body.List) == 0 {
		return false
	}
	_, ok := body.List[len(body.List)-1].(*ast.ReturnStmt)
	return ok
}

func callName(call *ast.CallExpr) string {
	return ast.Unparen(call.Fun).(*ast.SelectorExpr).Sel.Name
}
