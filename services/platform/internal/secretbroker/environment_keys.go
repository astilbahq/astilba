package secretbroker

import (
	"context"
	"strconv"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/google/uuid"
)

// DestroyEnvironmentKeys destroys an environment's keys in q, the transaction that purges the
// environment or ends its preview, and records each in the installation's key history. Every
// copy of a value sealed under them, wherever it is kept, no longer opens. A writer holding a key
// to store a value under it finishes first, and none stores one after. An environment with no key
// has nothing to destroy.
func (b *Broker) DestroyEnvironmentKeys(ctx context.Context, q *db.Queries, workspace, environment uuid.UUID, reason string) error {
	generations, err := q.DestroyEnvironmentKeys(ctx, db.DestroyEnvironmentKeysParams{WorkspaceID: workspace, EnvironmentID: environment})
	if err != nil {
		return err
	}
	run := uuid.New()
	for _, generation := range generations {
		if err = b.event(ctx, q, keyEvent{run: run, kind: "environment-key-destroyed", reason: reason, scope: custody.ScopeWorkspace, workspace: &workspace, details: map[string]string{
			"environment": environment.String(), "generation": strconv.FormatInt(generation, 10),
		}}); err != nil {
			return err
		}
	}
	return nil
}

// DestroyEndedEnvironmentKeys destroys, in q, the keys of a workspace's environments that are
// purging or purged, or are previews whose every request has ended, while any is still active.
// Recovery runs it after merging, since a checkpoint can bring a purge or end a preview that
// management's database has not seen, such as one restored from a backup taken before.
func (b *Broker) DestroyEndedEnvironmentKeys(ctx context.Context, q *db.Queries, workspace uuid.UUID) error {
	ended, err := q.EndedEnvironmentKeys(ctx, workspace)
	if err != nil {
		return err
	}
	for _, e := range ended {
		reason := "preview ended by recovery"
		if e.Purged {
			reason = "environment purge taken by recovery"
		}
		if err = b.DestroyEnvironmentKeys(ctx, q, workspace, e.EnvironmentID, reason); err != nil {
			return err
		}
	}
	return nil
}

// correctEnvironmentsStage records custody stage 5 again where stage 6, every environment's values
// under its own key, has stopped being true: an instance of an earlier release, which seals them
// under their workspace's key, has run since it was recorded, as rolling back to one and forward
// again does. At startup it also counts such values, which an earlier release may have stored
// while no instance of this one ran. Converting then moves them and records stage 6 again, and
// until it does, what purging reaches is described as at stage 5.
func (b *Broker) correctEnvironmentsStage(ctx context.Context, startup bool) error {
	if b.pool == nil {
		// Custody is not installed: there is no stage to correct.
		return nil
	}
	q := db.New(b.pool)
	older, err := q.InstancesBelowStageSinceRecorded(ctx, sealEnvironmentsStage)
	if err != nil {
		return err
	}
	reason := ""
	switch {
	case older > 0:
		reason = "an instance of a release before stage 6 ran since it was recorded"
	case startup:
		stage, err := q.CustodyStage(ctx)
		if err != nil || stage < sealEnvironmentsStage {
			return err
		}
		counts, err := q.LegacyValueCounts(ctx)
		if err != nil {
			return err
		}
		var outside int64
		for _, c := range counts {
			outside += c.Outside
		}
		if outside > 0 {
			reason = strconv.FormatInt(outside, 10) + " environment values are under their workspace's key"
		}
	}
	if reason == "" {
		return nil
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tq := db.New(tx)
	reverted, err := tq.RevertCustodyStage(ctx, db.RevertCustodyStageParams{Back: sealEnvironmentsStage - 1, Current: sealEnvironmentsStage})
	if err != nil || reverted == 0 {
		return err
	}
	if err = b.event(ctx, tq, keyEvent{run: uuid.New(), kind: "stage-reverted", reason: reason, details: map[string]string{"stage": strconv.Itoa(sealEnvironmentsStage - 1), "from": strconv.Itoa(sealEnvironmentsStage)}}); err != nil {
		return err
	}
	b.log.Warn("custody stage recorded as 5 again: run `astilba custody convert`", "reason", reason)
	return tx.Commit(ctx)
}
