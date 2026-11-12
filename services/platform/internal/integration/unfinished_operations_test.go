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

// operationStates is every state the operations table allows, read from its CHECK constraint.
func operationStates(t *testing.T, f *fixture) []string {
	t.Helper()
	var check string
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='execution_operations'::regclass AND conname='execution_operations_state_check'`).Scan(&check); err != nil {
		t.Fatal(err)
	}
	states := []string{}
	for _, state := range regexp.MustCompile(`'([a-z_]+)'::text`).FindAllStringSubmatch(check, -1) {
		states = append(states, state[1])
	}
	if len(states) == 0 {
		t.Fatalf("no states in %s", check)
	}
	return states
}

// execution_unfinished_server holds a host's unfinished operations, so its predicate admits exactly
// the states the table allows that don't end an operation. A state added to the table, or a change
// to which states end one, fails here until the index agrees.
func TestUnfinishedIndexHoldsExactlyTheStatesThatHaveNotEnded(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	var predicate string
	if err := f.st.Pool.QueryRow(ctx, `SELECT pg_get_expr(indpred,indrelid) FROM pg_index WHERE indexrelid='execution_unfinished_server'::regclass`).Scan(&predicate); err != nil {
		t.Fatal(err)
	}
	for _, state := range operationStates(t, f) {
		var indexed bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT `+predicate+` FROM (SELECT $1::text AS state) operation`, state).Scan(&indexed); err != nil {
			t.Fatal(err)
		}
		if ended := execution.Terminal(api.ExecutionState(state)); indexed == ended {
			t.Errorf("state %s ends an operation: %v; the index holds it: %v", state, ended, indexed)
		}
	}
}

// Every agent polls about once a second, and placement, upgrades, pipelines, networks and
// revocation ask whether a host has unfinished work. Operation rows are permanent, so a query that
// reads a host's operations must not read its history: the poll once did, and past PostgreSQL's
// JIT threshold each poll spent hundreds of milliseconds compiling. This takes every statement that
// reads a host's operations from the code, as the inline SQL check counts them, and sqlc's, so a
// new one is held to the same plan: no sequential scan of operations, and a cost that stays put
// as ended history triples.
func TestHostOperationQueriesDoNotReadHistory(t *testing.T) {
	ctx := context.Background()
	queries := hostOperationQueries(t)
	if len(queries) < 10 {
		t.Fatalf("found only %d statements reading a host's operations", len(queries))
	}
	t.Logf("%d statements read a host's operations", len(queries))
	f := setup(t)
	// Each lists the states it reads, so each list is one of these exactly, and a state added to
	// the table fails here until every query agrees. Excluding states would read history: the
	// planner estimates NOT IN as if most rows matched.
	unfinished, started := []string{}, []string{}
	for _, state := range operationStates(t, f) {
		if !execution.Terminal(api.ExecutionState(state)) {
			unfinished = append(unfinished, state)
			if state != "queued" {
				started = append(started, state)
			}
		}
	}
	slices.Sort(unfinished)
	slices.Sort(started)
	for _, q := range queries {
		for _, list := range stateLists.FindAllStringSubmatch(q.sql, -1) {
			states := []string{}
			for _, state := range quotedState.FindAllStringSubmatch(list[2], -1) {
				states = append(states, state[1])
			}
			slices.Sort(states)
			ended := !slices.ContainsFunc(states, func(state string) bool { return !execution.Terminal(api.ExecutionState(state)) })
			switch {
			case list[1] != "":
				t.Errorf("%s excludes states; list the unfinished ones: %s", q, list[0])
			case !ended && !slices.Equal(states, unfinished) && !slices.Equal(states, started):
				t.Errorf("%s reads %v, not the unfinished states %v or the started ones %v", q, states, unfinished, started)
			}
		}
	}
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
	// Ended operations on the one host, of several kinds and in every ended state.
	history := func(count int) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state,report_sequence,created_at)
SELECT gen_random_uuid(),$1,$2,1,gen_random_uuid(),'{}',(ARRAY['network','visual','crosshost','backup'])[1+i%4],(ARRAY['succeeded','failed','cancelled'])[1+i%3],1,now()-interval '1 day'-i*interval '1 second' FROM generate_series(1,$3::int) i`, ws, in.ServerId, count); err != nil {
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
			t.Errorf("%s: explaining: %v", q, err)
			return node{}, false
		}
		var plans []struct {
			Plan node `json:"Plan"`
		}
		if err := json.Unmarshal(results[0].Rows[0][0], &plans); err != nil || len(plans) != 1 {
			t.Errorf("%s: plan %s: %v", q, results[0].Rows[0][0], err)
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
	for _, r := range historyReaders {
		if !slices.ContainsFunc(queries, func(q hostOperationQuery) bool { return q.name == r.name && strings.Contains(q.sql, r.fragment) }) {
			t.Errorf("historyReaders names %s %q, which reads no host's operations now", r.name, r.fragment)
		}
	}
	history(20000)
	for _, q := range queries {
		p, ok := plan(q)
		if !ok {
			continue
		}
		reason, excepted := historyReader(q)
		read := scans(p) || p.Cost >= threshold || p.Cost > before[q.at]*1.05+1
		switch {
		case excepted && !read:
			t.Errorf("%s no longer reads history; take it out of historyReaders", q)
		case excepted:
			t.Logf("%s reads history: %s", q, reason)
		case scans(p):
			t.Errorf("%s reads every operation:\n%s", q, q.sql)
		case p.Cost >= threshold:
			t.Errorf("%s costs %.0f, past the JIT threshold of %.0f", q, p.Cost, threshold)
		case p.Cost > before[q.at]*1.05+1:
			t.Errorf("%s grows with history: %.0f, then %.0f with three times as much", q, before[q.at], p.Cost)
		}
	}
}

// historyReaders are the statements known to read a host's ended history, each named by its sqlc
// name or file and function, with a fragment of its SQL, and why it does.
var historyReaders = []struct{ name, fragment, reason string }{
	{"NetworkManifestMissed", "", "it looks up ended commands; PLAT-317 bounds it"},
	{"operations.go/(*worker).Work", "primary_child.parent_id", "it finds a parent's children, and operations have no parent index yet; PLAT-317 adds one"},
	{"recovery.go/(*Store).PrepareRecovery", "DELETE FROM river_job", "offline recovery drops the queued jobs of every operation the host ever ran"},
}

// historyReader is why a statement reads history, if it is known to.
func historyReader(q hostOperationQuery) (string, bool) {
	for _, r := range historyReaders {
		if q.name == r.name && strings.Contains(q.sql, r.fragment) {
			return r.reason, true
		}
	}
	return "", false
}

// hostOperationQuery is a statement, where it is, and its sqlc name if it has one.
type hostOperationQuery struct{ at, name, sql string }

func (q hostOperationQuery) String() string {
	if q.name == q.at {
		return q.at
	}
	return q.name + " (" + q.at + ")"
}

var (
	stateLists  = regexp.MustCompile(`\bstate (NOT )?IN ?\(([^)]*)\)`)
	quotedState = regexp.MustCompile(`'([a-z_]+)'`)
	// A literal that begins a statement, as scripts/inline-sql.mjs counts them, after sqlc's name.
	statement = regexp.MustCompile(`^\s*(?:-- name: (\w+)[^\n]*\n\s*)?(?:SELECT|INSERT|UPDATE|DELETE|WITH)\s`)
	byHost    = regexp.MustCompile(`\bserver_id ?= ?\$\d`)
)

// hostOperationQueries is every statement in the module's code, sqlc's generated ones included,
// that reads operations by host.
func hostOperationQueries(t *testing.T) []hostOperationQuery {
	t.Helper()
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
			for _, decl := range file.Decls {
				// A statement outside sqlc is named by its file and function, which stay put as
				// lines move.
				owner := filepath.Base(path)
				if fn, ok := decl.(*ast.FuncDecl); ok {
					owner += "/" + funcName(fn)
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					literal, ok := n.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						return true
					}
					sql, err := strconv.Unquote(literal.Value)
					if err != nil || !strings.Contains(sql, "execution_operations") || !byHost.MatchString(sql) {
						return true
					}
					if begins := statement.FindStringSubmatch(sql); begins != nil {
						name := begins[1]
						if name == "" {
							name = owner
						}
						found = append(found, hostOperationQuery{files.Position(literal.Pos()).String(), name, sql})
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return found
}

// funcName is a function's name, with its receiver's type for a method.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	receiver := fn.Recv.List[0].Type
	if star, ok := receiver.(*ast.StarExpr); ok {
		if ident, ok := star.X.(*ast.Ident); ok {
			return "(*" + ident.Name + ")." + fn.Name.Name
		}
	}
	if ident, ok := receiver.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
