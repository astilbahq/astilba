package integration

import (
	"context"
	"slices"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// phaseQuerier is a pool or a transaction.
type phaseQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// unrunPhases lists each coordinated phase management issued a host that the agent would refuse
// as unsupported. Every integration test checks its own database when it ends, so a phase added to
// management fails the tests that reach it until the agent classifies it.
func unrunPhases(t *testing.T, q phaseQuerier) []string {
	t.Helper()
	rows, err := q.Query(context.Background(), `SELECT DISTINCT command->'crosshost'->>'phase',COALESCE(jsonb_typeof(command->'crosshost'->'maintenance')='object',false) FROM execution_operations WHERE kind='crosshost' AND parent_id IS NOT NULL AND command->'crosshost'->>'phase' IS NOT NULL`)
	if err != nil {
		t.Error("reading issued coordinated phases:", err)
		return nil
	}
	defer rows.Close()
	unrun := []string{}
	for rows.Next() {
		var phase string
		var maintenance bool
		if err = rows.Scan(&phase, &maintenance); err != nil {
			t.Error(err)
			return nil
		}
		body := deployment.CrossHostCommand{Phase: phase}
		if maintenance {
			body.Maintenance = &deployment.Maintenance{}
		}
		if !agent.RunsCoordinatedPhase(body) {
			unrun = append(unrun, map[bool]string{true: "maintenance ", false: ""}[maintenance]+phase)
		}
	}
	if err = rows.Err(); err != nil {
		t.Error(err)
	}
	return unrun
}

// The check every test makes when it ends finds a phase the agent doesn't run.
func TestUnrunCoordinatedPhaseIsFound(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	_, in, _ := enrollment(t, f, b, ws, "deployment")
	if _, err := f.execution.Enroll(ctx, in, "test"); err != nil {
		t.Fatal(err)
	}
	// Planted in a transaction that never commits, so this test's own check passes.
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	parent := uuid.New()
	for _, row := range []struct {
		id      uuid.UUID
		parent  *uuid.UUID
		command string
	}{
		{parent, nil, `{}`},
		{uuid.New(), &parent, `{"crosshost":{"phase":"probe"}}`},
		{uuid.New(), &parent, `{"crosshost":{"phase":"drain"}}`},
		{uuid.New(), &parent, `{"crosshost":{"phase":"probe","maintenance":{}}}`},
	} {
		if _, err = tx.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state,parent_id) VALUES($1,$2,$3,1,$4,$5,'crosshost','succeeded',$6)`, row.id, ws, in.ServerId, uuid.New(), row.command, row.parent); err != nil {
			t.Fatal(err)
		}
	}
	unrun := unrunPhases(t, tx)
	slices.Sort(unrun)
	if !slices.Equal(unrun, []string{"drain", "maintenance probe"}) {
		t.Fatalf("unrun phases = %v", unrun)
	}
}
