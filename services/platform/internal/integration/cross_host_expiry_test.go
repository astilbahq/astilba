package integration

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

func testCrossHostExpiry(t *testing.T, f *fixture, b *browser, ws uuid.UUID, path string, plan deployment.Status, phase string, wait func(func() bool)) {
	t.Helper()
	ctx := context.Background()
	servingState := func() []byte {
		t.Helper()
		var raw []byte
		if err := f.st.Pool.QueryRow(ctx, `SELECT jsonb_build_object('revision',e.applied_revision_id,'runtime',r) FROM environments e JOIN visual_runtime r ON r.environment_id=e.id WHERE e.workspace_id=$1 AND e.id=$2`, ws, plan.Plan.EnvironmentID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	previous := servingState()
	// PLAT-62: expiry commits with the phase transition, before that phase can
	// create its first child. The probe transition follows every prepare receipt.
	if _, err := f.st.Pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION expire_cross_host_subscription() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NOT EXISTS(SELECT 1 FROM network_endpoint_sources s JOIN network_plan_sources p ON p.endpoint_id=s.endpoint_id AND p.server_id=s.server_id AND p.unit_id=s.unit_id WHERE p.plan_id=NEW.plan_id AND s.active) THEN
				RAISE EXCEPTION 'fixture requires active candidate grants before expiry';
			END IF;
			UPDATE hosted_subscriptions SET management_until=now()-interval '1 minute' WHERE workspace_id=NEW.workspace_id;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER expire_cross_host_subscription AFTER UPDATE OF phase ON cross_host_operations
		FOR EACH ROW WHEN (OLD.phase IS DISTINCT FROM NEW.phase AND NEW.phase='%s') EXECUTE FUNCTION expire_cross_host_subscription()`, phase)); err != nil {
		t.Fatal(err)
	}
	var parent api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &parent)
	if phase == "probe" {
		wait(func() bool {
			var cancelled bool
			if err := f.st.Pool.QueryRow(ctx, `SELECT state='cancelled' AND cancel_requested FROM execution_operations WHERE id=$1`, parent.Id).Scan(&cancelled); err != nil {
				t.Fatal(err)
			}
			return cancelled
		})
	} else {
		wait(func() bool {
			var denied bool
			if err := f.st.Pool.QueryRow(ctx, `SELECT h.phase='publish' AND COALESCE(cardinality(j.errors),0)>0 FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id JOIN river_job j ON j.id=o.job_id WHERE h.id=$1`, parent.Id).Scan(&denied); err != nil {
				t.Fatal(err)
			}
			return denied
		})
	}
	var prepared, published, aborted, restricted, reservations int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE command->'crosshost'->>'phase'='prepare' AND state='succeeded'),count(*) FILTER (WHERE command->'crosshost'->>'phase'='publish'),count(*) FILTER (WHERE command->'crosshost'->>'phase'='abort' AND state='succeeded'),count(*) FILTER (WHERE command->'network'->>'action'='restrict' AND state='succeeded') FROM execution_operations WHERE parent_id=$1`, parent.Id).Scan(&prepared, &published, &aborted, &restricted); err != nil {
		t.Fatal(err)
	}
	units := len(plan.Plan.Distributed.Units)
	if prepared != units || published != 0 || !bytes.Equal(previous, servingState()) {
		t.Fatalf("expiry changed serving state or crossed the publication boundary: prepared=%d published=%d", prepared, published)
	}
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM cross_host_reservations WHERE parent_id=$1`, parent.Id).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if phase == "probe" {
		if aborted != units || restricted != units || reservations != 0 {
			t.Fatalf("expiry left candidate cleanup incomplete: aborted=%d restricted=%d reservations=%d", aborted, restricted, reservations)
		}
		var sources int
		if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM network_endpoint_sources s JOIN network_plan_sources p ON p.endpoint_id=s.endpoint_id AND p.server_id=s.server_id AND p.unit_id=s.unit_id WHERE p.plan_id=$1 AND s.active`, plan.Plan.ID).Scan(&sources); err != nil || sources != 0 {
			t.Fatal("expiry left candidate-only network grants active", err)
		}
		return
	}
	var cancelled bool
	var state api.ExecutionState
	if err := f.st.Pool.QueryRow(ctx, `SELECT state,cancel_requested FROM execution_operations WHERE id=$1`, parent.Id).Scan(&state, &cancelled); err != nil {
		t.Fatal(err)
	}
	if aborted != 0 || restricted != 0 || reservations != units || cancelled || state == "succeeded" || state == "failed" || state == "cancelled" {
		t.Fatalf("publication boundary lost its reconciliation reservation: state=%s cancelled=%v aborted=%d restricted=%d reservations=%d", state, cancelled, aborted, restricted, reservations)
	}
	if err := f.st.GrantManagement(ctx, ws, time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		var succeeded bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT state='succeeded' FROM execution_operations WHERE id=$1`, parent.Id).Scan(&succeeded); err != nil {
			t.Fatal(err)
		}
		return succeeded
	})
	var applied bool
	if err := f.st.Pool.QueryRow(ctx, `SELECT e.applied_revision_id=$3 AND r.plan_id=$4 AND NOT EXISTS(SELECT 1 FROM cross_host_reservations WHERE parent_id=$5) AND EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$5 AND command->'crosshost'->>'phase'='publish' AND state='succeeded') AND NOT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$5 AND command->'crosshost'->>'phase'='abort') FROM environments e JOIN visual_runtime r ON r.environment_id=e.id WHERE e.workspace_id=$1 AND e.id=$2`, ws, plan.Plan.EnvironmentID, plan.Plan.RevisionID, plan.Plan.ID, parent.Id).Scan(&applied); err != nil || !applied {
		t.Fatal("renewal did not finish the same reserved deployment", err)
	}
}
