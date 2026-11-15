// Package workspaceinsert reports a workspace inserted other than by store.CreateWorkspace, which
// stores the workspace's keys in the same transaction. A workspace created any other way would
// have no keys until the background job made them.
package workspaceinsert

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "workspaceinsert",
	Doc:  "reports (*db.Queries).InsertWorkspace outside store.CreateWorkspace, which stores the workspace's keys with it",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	store := strings.HasSuffix(pass.Pkg.Path(), "/internal/store")
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && store && fn.Recv == nil && fn.Name.Name == "CreateWorkspace" {
				continue
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if ok && insertsWorkspace(pass.TypesInfo.Uses[sel.Sel]) {
					pass.Reportf(sel.Pos(), "a workspace created without its keys would have none until the background job made them; use store.CreateWorkspace")
				}
				return true
			})
		}
	}
	return nil, nil
}

func insertsWorkspace(object types.Object) bool {
	fn, ok := object.(*types.Func)
	if !ok || fn.Name() != "InsertWorkspace" {
		return false
	}
	recv := fn.Signature().Recv()
	if recv == nil {
		return false
	}
	t := recv.Type()
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := t.(*types.Named)
	return ok && named.Obj().Name() == "Queries" && strings.HasSuffix(named.Obj().Pkg().Path(), "/db/generated")
}
