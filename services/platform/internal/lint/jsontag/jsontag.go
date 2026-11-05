// Package jsontag reports a struct field read from JSON with no explicit member name. The
// encoding/json reader folds case, so an untagged ID reads "id", but encoding/json/v2 matches
// names exactly and would leave it zero with no error.
package jsontag

import (
	"go/ast"
	"go/types"
	"reflect"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

var Analyzer = &analysis.Analyzer{
	Name: "jsontag",
	Doc:  "reports struct fields read from JSON without a json tag naming the member",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	readers := responseReaders(pass)
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if target := readTarget(pass, call, readers); target != nil {
					check(pass, target, pass.TypesInfo.TypeOf(target), map[types.Type]bool{})
				}
			}
			return true
		})
	}
	return nil, nil
}

// responseReaders finds this package's functions that decode an HTTP response into a
// parameter, by that parameter's position, because their callers choose the struct. A helper
// that reads back records it wrote itself, as the agent's journal does, writes the same names
// it reads, so only network responses count.
func responseReaders(pass *analysis.Pass) map[types.Object]int {
	readers := map[types.Object]int{}
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !handlesResponse(pass, fn.Body) {
				continue
			}
			parameters := map[types.Object]int{}
			for _, field := range fn.Type.Params.List {
				for _, name := range field.Names {
					parameters[pass.TypesInfo.Defs[name]] = len(parameters)
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := readTarget(pass, call, nil).(*ast.Ident); ok {
						if position, forwarded := parameters[pass.TypesInfo.ObjectOf(id)]; forwarded {
							readers[pass.TypesInfo.Defs[fn.Name]] = position
						}
					}
				}
				return true
			})
		}
	}
	return readers
}

func handlesResponse(pass *analysis.Pass, body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if object := pass.TypesInfo.ObjectOf(id); object != nil {
				found = found || types.TypeString(object.Type(), nil) == "*net/http.Response"
			}
		}
		return !found
	})
	return found
}

// readTarget returns the argument a JSON reader fills, or nil when the call is not one.
func readTarget(pass *analysis.Pass, call *ast.CallExpr, readers map[types.Object]int) ast.Expr {
	fn, ok := typeutil.Callee(pass.TypesInfo, call).(*types.Func)
	if !ok || fn.Pkg() == nil {
		return nil
	}
	position := -1
	switch fn.Pkg().Path() + "." + fn.Name() {
	case "encoding/json.Unmarshal", "encoding/json/v2.Unmarshal", "encoding/json/v2.UnmarshalRead", "encoding/json/v2.UnmarshalDecode":
		position = 1
	case "encoding/json.Decode":
		position = 0
	default:
		if known, ok := readers[fn.Origin()]; ok {
			position = known
		}
	}
	// A call can spread one multi-value result across its arguments.
	if position < 0 || position >= len(call.Args) {
		return nil
	}
	return call.Args[position]
}

func check(pass *analysis.Pass, at ast.Node, t types.Type, seen map[types.Type]bool) {
	if seen[t] || readsItself(t) {
		return
	}
	seen[t] = true
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		check(pass, at, u.Elem(), seen)
	case *types.Slice:
		check(pass, at, u.Elem(), seen)
	case *types.Array:
		check(pass, at, u.Elem(), seen)
	case *types.Map:
		check(pass, at, u.Elem(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			field := u.Field(i)
			tag, tagged := reflect.StructTag(u.Tag(i)).Lookup("json")
			if !field.Exported() && !field.Embedded() || tagged && tag == "-" {
				continue
			}
			name, options, _ := strings.Cut(tag, ",")
			// An unnamed embedded struct is inlined by both readers, and encoding/json/v2's
			// embed option forbids a name.
			if name == "" && !field.Embedded() && !slices.Contains(strings.Split(options, ","), "embed") {
				pass.Reportf(at.Pos(), "field %s is read from JSON with no json tag; encoding/json/v2 matches names exactly, so name its member", field.Name())
			}
			check(pass, at, field.Type(), seen)
		}
	}
}

func readsItself(t types.Type) bool {
	for _, method := range []string{"UnmarshalJSON", "UnmarshalJSONFrom", "UnmarshalText"} {
		object, _, _ := types.LookupFieldOrMethod(types.NewPointer(t), true, nil, method)
		if _, ok := object.(*types.Func); ok {
			return true
		}
	}
	return false
}
