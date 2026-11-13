package execution

import (
	"context"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// runnerEligible reports whether a runner can take work, and whether it is online. build asks also
// for what every build needs of its runner, which insertPipeline checks again of the runner chosen.
func runnerEligible(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, architecture string, controls, railpack, build bool) (bool, error) {
	var role string
	var raw []byte
	var online bool
	err := tx.QueryRow(ctx, "SELECT role,capabilities,COALESCE(last_seen>now()-interval '45 seconds',false) FROM servers WHERE workspace_id=$1 AND id=$2 AND NOT revoked AND credential_hash IS NOT NULL", ws, id).Scan(&role, &raw, &online)
	if err != nil {
		return false, err
	}
	var c api.AgentCapabilities
	if json.Unmarshal(raw, &c) != nil || role == "deployment" || c.Build == nil || !*c.Build || !c.Docker || (architecture != "" && architecture != string(c.Arch)) {
		return false, domain.Invalid("runner")
	}
	if controls && (c.BuildControls == nil || !*c.BuildControls) {
		return false, domain.Invalid("runner.buildControlsUpgradeRequired")
	}
	if railpack && (c.RailpackVersion == nil || *c.RailpackVersion != pipeline.RailpackVersion) {
		return false, domain.Invalid("runner.railpackUpgradeRequired")
	}
	if build && (c.BuildNetwork == nil || !*c.BuildNetwork) {
		return false, domain.Invalid("runner.buildNetworkUpgradeRequired")
	}
	if build && (c.RootlessBuilds == nil || !*c.RootlessBuilds) {
		return false, domain.Invalid("runner.rootlessBuildsUpgradeRequired")
	}
	return online, nil
}

// unknownFallbackRunner refuses saving a configuration whose fallback runner isn't one of the
// workspace's, or was revoked. Choosing a runner passes over a fallback that can't take work, so
// nothing else would say so.
var unknownFallbackRunner = &domain.Error{Code: "invalid_input", Message: "The fallback runner isn't one of this workspace's servers, or was revoked. Choose another fallback, or none.", Fields: []string{"configuration.fallbackRunnerId"}}

func checkFallbackRunner(ctx context.Context, tx pgx.Tx, ws uuid.UUID, fallback *uuid.UUID) error {
	if fallback == nil {
		return nil
	}
	known, err := db.New(tx).WorkspaceRunnerKnown(ctx, db.WorkspaceRunnerKnownParams{WorkspaceID: ws, ID: *fallback})
	if err == nil && !known {
		return unknownFallbackRunner
	}
	return err
}

// selectRunner chooses the runner for work: the primary, or its fallback when the primary is
// offline or busy and the fallback is ready. build is whether the work is a build.
func selectRunner(ctx context.Context, tx pgx.Tx, ws, primary uuid.UUID, fallback *uuid.UUID, architecture string, controls, railpack, build bool) (uuid.UUID, error) {
	online, err := runnerEligible(ctx, tx, ws, primary, architecture, controls, railpack, build)
	if err != nil {
		return uuid.Nil, err
	}
	var busy bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state IN ('queued','dispatched','running','needs_attention'))", primary).Scan(&busy); err != nil {
		return uuid.Nil, err
	}
	if fallback != nil && (!online || busy) {
		// A fallback that can't take the work, as when it was revoked or needs an upgrade, isn't
		// ready, so the primary queues it, as it would with no fallback: whether work starts must
		// not depend on how busy the primary is.
		ready, err := runnerEligible(ctx, tx, ws, *fallback, architecture, controls, railpack, build)
		var refused *domain.Error
		if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &refused) {
			return primary, nil
		}
		if err != nil {
			return uuid.Nil, err
		}
		if ready {
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state IN ('queued','dispatched','running','needs_attention'))", *fallback).Scan(&busy); err != nil {
				return uuid.Nil, err
			}
			if !busy {
				return *fallback, nil
			}
		}
	}
	return primary, nil
}
