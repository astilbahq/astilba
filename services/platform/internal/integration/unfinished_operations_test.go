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
	"github.com/jackc/pgx/v5"
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

// Operation rows are permanent, so a statement that reads operations must not read their history:
// agents poll about once a second, and placement, upgrades, pipelines, networks, previews,
// coordinators and sweeps ask about unfinished work or a root's children on every pass. The poll
// once read every operation, and past PostgreSQL's JIT threshold each poll spent hundreds of
// milliseconds compiling. This takes every statement that reads operations from the code, as the
// inline SQL check counts them, and sqlc's, so a new one is held to the same plan: no sequential
// scan of operations, and a cost that stays put as ended history triples. A statement assembled
// from several literals at run time, such as a recovery base's capture, isn't seen here (PLAT-325).
func TestOperationQueriesDoNotReadHistory(t *testing.T) {
	ctx := context.Background()
	queries := operationQueries(t)
	if len(queries) < 100 {
		t.Fatalf("found only %d statements reading operations", len(queries))
	}
	t.Logf("%d statements read operations", len(queries))
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
	// A list may also be one a partial index holds, as the alert sweep reads its candidates.
	indexed := [][]string{}
	predicates, err := f.st.Pool.Query(ctx, `SELECT pg_get_expr(indpred,indrelid) FROM pg_index WHERE indrelid='execution_operations'::regclass AND indpred IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := pgx.CollectRows(predicates, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, predicate := range listed {
		for _, list := range indexedStates.FindAllStringSubmatch(predicate, -1) {
			states := []string{}
			for _, state := range quotedState.FindAllStringSubmatch(list[1], -1) {
				states = append(states, state[1])
			}
			slices.Sort(states)
			indexed = append(indexed, states)
		}
	}
	// Other tables with a state of their own, whose lists aren't an operation's.
	rows, err := f.st.Pool.Query(ctx, `SELECT table_name FROM information_schema.columns WHERE table_schema='public' AND column_name='state' AND table_name<>'execution_operations'`)
	if err != nil {
		t.Fatal(err)
	}
	stateful, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queries {
		if other := otherState.FindString(q.sql); other != "" {
			t.Errorf("%s compares the state with %q: list the operation states it reads with state IN (...), as PostgreSQL estimates arrays as if most rows matched", q, other)
		}
		for _, list := range operationStateLists(q.sql, stateful) {
			states := []string{}
			for _, state := range quotedState.FindAllStringSubmatch(list[3], -1) {
				states = append(states, state[1])
			}
			slices.Sort(states)
			ended := !slices.ContainsFunc(states, func(state string) bool { return !execution.Terminal(api.ExecutionState(state)) })
			switch {
			case len(states) == 0:
				t.Errorf("%s lists no states it can be checked against: %s", q, list[0])
			case list[1] != "" || list[2] != "":
				t.Errorf("%s excludes operation states with NOT IN, which PostgreSQL estimates as if most rows matched, so it reads history: write state IN ('%s') instead of %s", q, strings.Join(unfinished, "','"), list[0])
			case !ended && !slices.Equal(states, unfinished) && !slices.Equal(states, started) && !slices.ContainsFunc(indexed, func(list []string) bool { return slices.Equal(states, list) }):
				t.Errorf("%s reads states %v: list exactly the unfinished states %v, the started ones %v or a list a partial index holds, so an index serves it", q, states, unfinished, started)
			}
		}
	}
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	_, in, _ := enrollment(t, f, b, ws, "deployment")
	if _, err := f.execution.Enroll(ctx, in, "test"); err != nil {
		t.Fatal(err)
	}
	env := envForExecution(t, f, b, ws)
	var threshold float64
	if err := f.st.Pool.QueryRow(ctx, `SELECT current_setting('jit_above_cost')::float8`).Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	// History as a long-lived environment on one host leaves it: deployments, coordinated roots
	// with their children, network configure commands at each generation, backups and builds, in
	// every ended state. Generations count up as the host's and environment's do.
	generation := 0
	history := func(count int) {
		t.Helper()
		roots := count / 8
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,created_at)
SELECT gen_random_uuid(),$1,$2,$3,$4+i,gen_random_uuid(),'{}','crosshost',(ARRAY['succeeded','failed','cancelled'])[1+i%3],1,now()-interval '1 day'-i*interval '1 second' FROM generate_series(1,$5::int) i`, ws, in.ServerId, env, generation, roots); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,parent_id,generation,request_key,command,kind,state,report_sequence,created_at)
SELECT gen_random_uuid(),$1,$2,$3,root.id,$4+row_number() OVER (),gen_random_uuid(),'{"crosshost":{"phase":"prepare"}}','crosshost',root.state,1,root.created_at
FROM execution_operations root, generate_series(1,3) child WHERE root.workspace_id=$1 AND root.kind='crosshost' AND root.parent_id IS NULL AND root.generation>$5`, ws, in.ServerId, env, generation+roots, generation); err != nil {
			t.Fatal(err)
		}
		// Deployments carry plans, as real ones do. The requests behind them would need a deployment
		// of their own, so the seed writes the plans without them.
		seed, err := f.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer seed.Rollback(ctx)
		if _, err := seed.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,visual_plan_id,created_at)
SELECT gen_random_uuid(),$1,$2,CASE WHEN kind<>'network' THEN $3::uuid END,$4+i,gen_random_uuid(),
    CASE kind WHEN 'network' THEN jsonb_build_object('network',jsonb_build_object('action','configure','membershipId',gen_random_uuid(),'manifest',jsonb_build_object('generation',$4+i)))
        WHEN 'visual' THEN '{"visual":{"action":"deploy"}}' ELSE '{}' END,
    kind,(ARRAY['succeeded','failed','cancelled'])[1+i%3],1,CASE WHEN kind='visual' THEN gen_random_uuid() END,now()-interval '1 day'-i*interval '1 second'
FROM generate_series(1,$5::int) i, LATERAL (SELECT (ARRAY['network','visual','backup','pipeline'])[1+i%4] AS kind) k`, ws, in.ServerId, env, generation+4*roots, count-4*roots); err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Exec(ctx, `INSERT INTO visual_plans(id,workspace_id,environment_id,plan) SELECT visual_plan_id,workspace_id,environment_id,'{}' FROM execution_operations WHERE workspace_id=$1 AND visual_plan_id IS NOT NULL AND generation>$2`, ws, generation); err != nil {
			t.Fatal(err)
		}
		if err := seed.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		generation += count
		if _, err := f.st.Pool.Exec(ctx, `ANALYZE execution_operations`); err != nil {
			t.Fatal(err)
		}
	}
	type node struct {
		Type     string  `json:"Node Type"`
		Relation string  `json:"Relation Name"`
		Index    string  `json:"Index Name"`
		Cost     float64 `json:"Total Cost"`
		Plans    []node  `json:"Plans"`
	}
	var scans func(node) bool
	scans = func(n node) bool {
		return n.Type == "Seq Scan" && n.Relation == "execution_operations" || slices.ContainsFunc(n.Plans, scans)
	}
	var uses func(node, string) bool
	uses = func(n node, index string) bool {
		return n.Index == index || slices.ContainsFunc(n.Plans, func(c node) bool { return uses(c, index) })
	}
	// Statements in recovery and export read the temporary tables their transaction creates, so
	// they are explained in one that has them too.
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, create := range temporaryTables(t) {
		if _, err := tx.Exec(ctx, create); err != nil {
			t.Fatalf("%s: %v", create, err)
		}
	}
	// A prepared statement runs on the generic plan after a few executions. Its parameters stay
	// unbound, so it goes as plain text, each behind a savepoint so one that fails leaves the rest.
	plan := func(q operationQuery) (node, bool) {
		t.Helper()
		if _, err := tx.Exec(ctx, `SAVEPOINT explaining`); err != nil {
			t.Fatal(err)
		}
		results, err := tx.Conn().PgConn().Exec(ctx, `EXPLAIN (GENERIC_PLAN, FORMAT JSON) `+q.sql).ReadAll()
		if _, rollback := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT explaining`); rollback != nil {
			t.Fatal(rollback)
		}
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
	// River's queue holds the jobs it finished in the last day, as a running installation's does.
	// Recovery's statements read it, and the few jobs the fixture's workers add as the test runs
	// then hardly move their plans.
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO river_job(kind,args,state,attempt,max_attempts,attempted_at,finalized_at,queue,priority)
SELECT 'platform_execution_v1',jsonb_build_object('id',id),'completed',1,25,now(),now(),'default',1 FROM execution_operations WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 2000`, ws); err != nil {
		t.Fatal(err)
	}
	// Every other table keeps the statistics it has now, so only the history the seed adds changes
	// between the two plans.
	if _, err := f.st.Pool.Exec(ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	before := map[string]float64{}
	for _, q := range queries {
		if p, ok := plan(q); ok {
			before[q.at] = p.Cost
		}
	}
	for _, r := range append(slices.Clone(historyReaders), limitedReaders...) {
		if !slices.ContainsFunc(queries, func(q operationQuery) bool { return q.name == r.name && strings.Contains(q.sql, r.fragment) }) {
			t.Errorf("%s %q is listed as reading history, but no statement reading operations matches it now", r.name, r.fragment)
		}
	}
	history(20000)
	for _, q := range queries {
		p, ok := plan(q)
		if !ok {
			continue
		}
		excepted, isExcepted := historyReader(q, historyReaders)
		bound, limited := historyReader(q, limitedReaders)
		// Index descents deepen as history grows, so a plan of index probes rises a few percent;
		// one that reads history roughly triples.
		grows := p.Cost > before[q.at]*1.1+1
		switch {
		case isExcepted && !scans(p) && p.Cost < threshold && !grows:
			t.Errorf("%s no longer reads history; take it out of historyReaders", q)
		case isExcepted:
			t.Logf("%s reads history: %s", q, excepted.reason)
		case scans(p):
			t.Errorf("%s reads every operation with a sequential scan: narrow it with an index or bound it with a LIMIT, or list it in historyReaders with a task if it must read history", q)
		case p.Cost >= threshold:
			t.Errorf("%s costs %.0f, past the JIT threshold of %.0f: narrow it with an index so every call doesn't compile", q, p.Cost, threshold)
		case limited && !uses(p, bound.index):
			t.Errorf("%s no longer reads through %s, which bounds it at its LIMIT: give it that index back, or another that keeps it bounded", q, bound.index)
		case limited && !grows:
			t.Errorf("%s no longer grows; take it out of limitedReaders", q)
		case limited:
			t.Logf("%s grows from %.0f to %.0f until %s", q, before[q.at], p.Cost, bound.reason)
		case grows:
			t.Errorf("%s grows with ended history (cost %.0f, then %.0f with three times as much): narrow it with an index or bound it with a LIMIT, or list it in historyReaders with a task if it must read history", q, before[q.at], p.Cost)
		}
	}
}

// historyReaderEntry is a statement named by its sqlc name or file and function, with a fragment
// of its SQL, why it is listed, and for one a LIMIT bounds, the index it reads in that order.
type historyReaderEntry struct{ name, fragment, reason, index string }

// historyReaders are the statements known to read a workspace's ended history, and why they may.
var historyReaders = []historyReaderEntry{
	{"import.go/(*Service).Import", "alert_observed=true", "it marks what it imported, which the archive limit bounds, in the workspace it creates", ""},
	{"ClearWorkspaceOperationJobs", "", "offline recovery forgets every operation's River job; PLAT-323 bounds it", ""},
	{"StampOperationControllerGenerations", "", "offline recovery stamps every operation with its host's controller generation; PLAT-323 bounds it", ""},
	{"RaiseServerUpdateGenerations", "", "offline recovery raises each host's generation past every command it ran; PLAT-323 bounds it", ""},
}

// limitedReaders are statements whose estimate rises with history only until a LIMIT caps it,
// because the planner can't tell how many rows match a command's JSON and guesses few. They are
// still held to the JIT threshold, and to the index that returns rows in the LIMIT's order.
var limitedReaders = []historyReaderEntry{
	{"export_selection.go", "ORDER BY o.created_at DESC,o.id DESC LIMIT 20", "it reaches each environment's 20 newest deployments", "execution_releases"},
	{"releases.go/(*Service).ReleaseHistory", "LIMIT 20", "it reaches the environment's 20 newest deployments", "execution_releases"},
}

// historyReader is the entry listing a statement, if one does.
func historyReader(q operationQuery, readers []historyReaderEntry) (historyReaderEntry, bool) {
	for _, r := range readers {
		if q.name == r.name && strings.Contains(q.sql, r.fragment) {
			return r, true
		}
	}
	return historyReaderEntry{}, false
}

// operationQuery is a statement, where it is, and its sqlc name if it has one.
type operationQuery struct{ at, name, sql string }

func (q operationQuery) String() string {
	if q.name == q.at {
		return q.at
	}
	return q.name + " (" + q.at + ")"
}

var (
	// A state list, and whether it excludes: NOT before the column or before IN.
	stateLists  = regexp.MustCompile(`(?i)(\bNOT\s*\(?\s*)?(?:(\w+)\.)?\bstate\s+(NOT\s+)?IN\s*\(([^)]*)\)`)
	quotedState = regexp.MustCompile(`'([a-z_]+)'`)
	// A list of states an index predicate holds, as PostgreSQL prints it.
	indexedStates = regexp.MustCompile(`\(state = ANY \(ARRAY\[([^\]]*)\]\)\)`)
	// A state list written as an array, which these statements don't use.
	otherState = regexp.MustCompile(`(?i)\bstate\s*(?:=\s*ANY|<>\s*ALL|!=\s*ALL)`)
	// A literal that begins a statement, as scripts/inline-sql.mjs counts them, after sqlc's name.
	statement = regexp.MustCompile(`^\s*(?:-- name: (\w+)[^\n]*\n\s*)?(?:SELECT|INSERT|UPDATE|DELETE|WITH)\s`)
	// The names a statement gives operations, and the tables it reads with a state column.
	operationAlias = regexp.MustCompile(`(?i)\bexecution_operations(?:\s+(?:AS\s+)?(\w+))?`)
	otherTable     = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|UPDATE)\s+(\w+)`)
	temporaryTable = regexp.MustCompile(`(?i)^\s*CREATE\s+TEMP(?:ORARY)?\s+TABLE\s`)
)

// operationStateLists is each state list in a statement that reads an operation's state, with its
// excluding NOT, its qualifier and its states in that order. A list on another table's state is
// that table's business: the state is an operation's when it is qualified by a name the statement
// gives operations, or unqualified when no other table the statement reads has a state.
func operationStateLists(sql string, stateful []string) [][]string {
	aliases := map[string]bool{"execution_operations": true}
	for _, m := range operationAlias.FindAllStringSubmatch(sql, -1) {
		aliases[strings.ToLower(m[1])] = m[1] != ""
	}
	alone := !slices.ContainsFunc(otherTable.FindAllStringSubmatch(sql, -1), func(m []string) bool {
		return !strings.EqualFold(m[1], "execution_operations") && slices.Contains(stateful, strings.ToLower(m[1]))
	})
	lists := [][]string{}
	for _, m := range stateLists.FindAllStringSubmatch(sql, -1) {
		if m[2] == "" && alone || m[2] != "" && aliases[strings.ToLower(m[2])] {
			lists = append(lists, []string{m[0], m[1], m[3], m[4]})
		}
	}
	return lists
}

// operationQueries is every statement in the module's code, sqlc's generated ones included, that
// reads operations.
func operationQueries(t *testing.T) []operationQuery {
	t.Helper()
	found := []operationQuery{}
	walkLiterals(t, func(at, owner, sql string) {
		if !strings.Contains(sql, "execution_operations") {
			return
		}
		if begins := statement.FindStringSubmatch(sql); begins != nil {
			name := begins[1]
			if name == "" {
				name = owner
			}
			found = append(found, operationQuery{at, name, sql})
		}
	})
	return found
}

// temporaryTables is each temporary table a statement in the module's code creates.
func temporaryTables(t *testing.T) []string {
	t.Helper()
	found := []string{}
	walkLiterals(t, func(_, _, sql string) {
		for _, s := range strings.Split(sql, ";") {
			if temporaryTable.MatchString(s) && !slices.Contains(found, s) {
				found = append(found, s)
			}
		}
	})
	return found
}

// walkLiterals calls each with every string literal in the module's code, sqlc's generated code
// included, where it is and the function it is in.
func walkLiterals(t *testing.T, each func(at, owner, literal string)) {
	t.Helper()
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
					if value, err := strconv.Unquote(literal.Value); err == nil {
						each(files.Position(literal.Pos()).String(), owner, value)
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
