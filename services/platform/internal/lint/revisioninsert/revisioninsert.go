// Package revisioninsert reports a specification revision written other than through
// store.InsertRevision, which refuses an oversized specification before the table's size check
// fails the transaction.
package revisioninsert

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "revisioninsert",
	Doc:  "reports (*db.Queries).InsertRevision outside package store, which owns the size check",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	if strings.HasSuffix(pass.Pkg.Path(), "/internal/store") {
		return nil, nil
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			fn, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Func)
			if !ok || fn.Name() != "InsertRevision" {
				return true
			}
			recv := fn.Signature().Recv()
			if recv == nil {
				return true
			}
			t := recv.Type()
			if pointer, ok := t.(*types.Pointer); ok {
				t = pointer.Elem()
			}
			if named, ok := t.(*types.Named); ok && named.Obj().Name() == "Queries" && strings.HasSuffix(named.Obj().Pkg().Path(), "/db/generated") {
				pass.Reportf(sel.Pos(), "a specification over the size limit would fail the table's check and abort the transaction; use store.InsertRevision")
			}
			return true
		})
	}
	return nil, nil
}
