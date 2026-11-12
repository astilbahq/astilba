package integration

import (
	"context"
	"encoding/json/v2"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
)

// execution_unfinished_server holds a host's unfinished operations, so its predicate admits exactly
// the states the table allows that don't end an operation. A state added to the table, or a change
// to which states end one, fails here until the index agrees.
func TestUnfinishedIndexHoldsExactlyTheStatesThatHaveNotEnded(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	var check, predicate string
	if err := f.st.Pool.QueryRow(ctx, `SELECT pg_get_constraintdef(c.oid),(SELECT pg_get_expr(i.indpred,i.indrelid) FROM pg_index i WHERE i.indexrelid='execution_unfinished_server'::regclass) FROM pg_constraint c WHERE c.conrelid='execution_operations'::regclass AND c.conname='execution_operations_state_check'`).Scan(&check, &predicate); err != nil {
		t.Fatal(err)
	}
	states := regexp.MustCompile(`'([a-z_]+)'::text`).FindAllStringSubmatch(check, -1)
	if len(states) == 0 {
		t.Fatalf("no states in %s", check)
	}
	for _, state := range states {
		var indexed bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT `+predicate+` FROM (SELECT $1::text AS state) operation`, state[1]).Scan(&indexed); err != nil {
			t.Fatal(err)
		}
		if ended := execution.Terminal(api.ExecutionState(state[1])); indexed == ended {
			t.Errorf("state %s ends an operation: %v; the index holds it: %v", state[1], ended, indexed)
		}
	}
}

// Every agent polls about once a second, and placement, upgrades, pipelines, networks and
// revocation ask whether a host has unfinished work. Operation rows are permanent, so a query that
// reads a host's operations by state must not read its history: the poll once did, and past
// PostgreSQL's JIT threshold each poll spent hundreds of milliseconds compiling. This finds every
// such query in the source, so a new one is held to the same plan: no sequential scan of
// operations, and a cost that stays put as ended history triples.
func TestHostOperationQueriesDoNotReadHistory(t *testing.T) {
	ctx := context.Background()
	queries := hostOperationQueries(t)
	if len(queries) < 10 {
		t.Fatalf("found only %d queries reading a host's unfinished operations", len(queries))
	}
	t.Logf("%d queries read a host's unfinished operations", len(queries))
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	_, in, _ := enrollment(t, f, b, ws, "deployment")
	if _, err := f.execution.Enroll(ctx, in, "test"); err != nil {
		t.Fatal(err)
	}
	var threshold float64
	if err := f.st.Pool.QueryRow(ctx, `SELECT current_setting('jit_above_cost')::float8`).Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	conn, err := f.st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// Ended operations on the one host, in every ended state.
	history := func(count int) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state,report_sequence,created_at)
SELECT gen_random_uuid(),$1,$2,1,gen_random_uuid(),'{}','network',(ARRAY['succeeded','failed','cancelled'])[1+i%3],1,now()-interval '1 day'-i*interval '1 second' FROM generate_series(1,$3::int) i`, ws, in.ServerId, count); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.Pool.Exec(ctx, `ANALYZE execution_operations`); err != nil {
			t.Fatal(err)
		}
	}
	type node struct {
		Type     string  `json:"Node Type"`
		Relation string  `json:"Relation Name"`
		Cost     float64 `json:"Total Cost"`
		Plans    []node  `json:"Plans"`
	}
	var scans func(node) bool
	scans = func(n node) bool {
		return n.Type == "Seq Scan" && n.Relation == "execution_operations" || slices.ContainsFunc(n.Plans, scans)
	}
	// A prepared statement runs on the generic plan after a few executions. Its parameters stay
	// unbound, so it goes as plain text.
	plan := func(q hostOperationQuery) (node, bool) {
		t.Helper()
		results, err := conn.Conn().PgConn().Exec(ctx, `EXPLAIN (GENERIC_PLAN, FORMAT JSON) `+q.sql).ReadAll()
		if err != nil || len(results) != 1 || len(results[0].Rows) != 1 {
			t.Errorf("%s: explaining: %v", q.at, err)
			return node{}, false
		}
		var plans []struct {
			Plan node `json:"Plan"`
		}
		if err := json.Unmarshal(results[0].Rows[0][0], &plans); err != nil || len(plans) != 1 {
			t.Errorf("%s: plan %s: %v", q.at, results[0].Rows[0][0], err)
			return node{}, false
		}
		return plans[0].Plan, true
	}
	history(10000)
	before := map[string]float64{}
	for _, q := range queries {
		if p, ok := plan(q); ok {
			before[q.at] = p.Cost
		}
	}
	history(20000)
	for _, q := range queries {
		p, ok := plan(q)
		if !ok {
			continue
		}
		switch {
		case scans(p):
			t.Errorf("%s reads every operation: %s", q.at, q.sql)
		case p.Cost >= threshold:
			t.Errorf("%s costs %.0f, past the JIT threshold of %.0f", q.at, p.Cost, threshold)
		case p.Cost > before[q.at]*1.05+1:
			t.Errorf("%s grows with history: %.0f, then %.0f with three times as much", q.at, before[q.at], p.Cost)
		}
	}
}

type hostOperationQuery struct{ at, sql string }

// hostOperationQueries is every SQL literal in the module's code, sqlc's included, that reads a
// host's operations for unfinished ones: it excludes states, or names one that hasn't ended.
// Lookups of ended operations alone read history by design, and PLAT-317 bounds them.
func hostOperationQueries(t *testing.T) []hostOperationQuery {
	t.Helper()
	host := regexp.MustCompile(`\bserver_id ?= ?\$\d`)
	states := regexp.MustCompile(`\bstate (NOT )?IN ?\(([^)]*)\)`)
	unfinished := func(sql string) bool {
		for _, list := range states.FindAllStringSubmatch(sql, -1) {
			if list[1] != "" {
				return true
			}
			for _, state := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(list[2], -1) {
				if !execution.Terminal(api.ExecutionState(state[1])) {
					return true
				}
			}
		}
		return false
	}
	found := []hostOperationQuery{}
	for _, root := range []string{"..", "../../db/generated"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || path == "../integration" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			files := token.NewFileSet()
			file, err := parser.ParseFile(files, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				literal, ok := n.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				sql, err := strconv.Unquote(literal.Value)
				if err == nil && strings.Contains(sql, "execution_operations") && host.MatchString(sql) && unfinished(sql) {
					found = append(found, hostOperationQuery{files.Position(literal.Pos()).String(), sql})
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return found
}
