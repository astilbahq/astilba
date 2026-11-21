package deadline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// Every command kind the contract defines has a deadline rule, and the agent runs no other, so a
// new kind needs a decision about its deadline before it can run at all.
func TestEveryCommandKindHasADeadlineRule(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../api/api.gen.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[api.ExecutionCommandKind]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if kind, ok := spec.Type.(*ast.Ident); ok && kind.Name == "ExecutionCommandKind" {
			for _, v := range spec.Values {
				value, err := strconv.Unquote(v.(*ast.BasicLit).Value)
				if err != nil {
					t.Fatal(err)
				}
				kinds[api.ExecutionCommandKind(value)] = true
			}
		}
		return true
	})
	if len(kinds) == 0 {
		t.Fatal("found no command kinds in the contract")
	}
	for kind := range kinds {
		if _, ok := kinds[kind]; !ok {
			t.Errorf("command kind %s has no deadline rule", kind)
		}
	}
	for kind := range kinds {
		if !kinds[kind] {
			t.Errorf("deadline rule for command kind %s, which the contract doesn't define", kind)
		}
	}
}

// Management sends a late command without values only when its rule runs nothing late. Every rule
// that ends or narrows what a host holds still needs what it came with.
func TestOnlyRulesThatRunNothingLateLapse(t *testing.T) {
	for rule, lapses := range map[Rule]bool{StartsWork: true, OwnDeadline: true, NarrowsAccess: false, ExtendsHeldRoot: false, EndsHeldWork: false, EndsHeldWorkOrNone: false, RetiresHeldUnit: false, BeforeAdmission: false} {
		if rule.Lapses() != lapses {
			t.Errorf("rule %d lapses: %v", rule, rule.Lapses())
		}
	}
}

// Management's verdict decides, whatever this clock reads, and this clock decides on its own when
// management has said nothing.
func TestPassedTakesManagementsVerdict(t *testing.T) {
	expired, timely := true, false
	for _, tc := range []struct {
		deadline time.Time
		expired  *bool
		passed   bool
	}{
		{time.Now().Add(time.Hour), nil, false},
		{time.Now().Add(time.Hour), &timely, false},
		{time.Now().Add(time.Hour), &expired, true},
		{time.Now().Add(-time.Hour), nil, true},
		{time.Now().Add(-time.Hour), &timely, true},
	} {
		if Passed(api.ExecutionCommand{Deadline: tc.deadline, Expired: tc.expired}) != tc.passed {
			t.Errorf("deadline %v, expired %v: passed %v", tc.deadline, tc.expired, !tc.passed)
		}
	}
}
