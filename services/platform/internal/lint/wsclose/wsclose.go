// Package wsclose reports a websocket close handshake with no time limit.
package wsclose

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "wsclose",
	Doc:  "reports (*websocket.Conn).Close outside package terminal, which owns the limited close",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	if strings.HasSuffix(pass.Pkg.Path(), "/internal/terminal") {
		return nil, nil
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			fn, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Func)
			if ok && fn.FullName() == "(*github.com/coder/websocket.Conn).Close" {
				pass.Reportf(sel.Pos(), "a peer that stalls inside a frame holds this close forever; use terminal.Close")
			}
			return true
		})
	}
	return nil, nil
}
