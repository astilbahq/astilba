package secretbroker

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	if stage, err := db.New(b.pool).CustodyStage(ctx); err != nil || stage != sealEnvironmentsStage {
		return err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	// Holding the stage while checking keeps converting from recording it anew in between, which
	// the check would otherwise revert for an instance seen before it was.
	if stage, err := q.LockCustodyStage(ctx); err != nil || stage != sealEnvironmentsStage {
		return err
	}
	older, err := q.InstancesBelowStageSinceRecorded(ctx, sealEnvironmentsStage)
	if err != nil {
		return err
	}
	reason := ""
	switch {
	case older > 0:
		reason = "an instance of a release before stage 6 ran since it was recorded"
	case startup:
		outside, err := q.EnvironmentValuesOutsideTheirKeys(ctx)
		if err != nil {
			return err
		}
		if outside {
			reason = "environment values are under their workspace's key"
		}
	}
	if reason == "" {
		return nil
	}
	if err = q.RevertCustodyStage(ctx, db.RevertCustodyStageParams{Back: sealEnvironmentsStage - 1, Current: sealEnvironmentsStage}); err != nil {
		return err
	}
	if err = b.event(ctx, q, keyEvent{run: uuid.New(), kind: "stage-reverted", reason: reason, details: map[string]string{"stage": strconv.Itoa(sealEnvironmentsStage - 1), "from": strconv.Itoa(sealEnvironmentsStage)}}); err != nil {
		return err
	}
	b.log.Warn("custody stage recorded as 5 again: run `astilba custody convert`", "reason", reason)
	return tx.Commit(ctx)
}

// environmentSealingKey is the key an environment's new values are sealed under, held in q, the
// writer's transaction. The environment's first value makes it, wrapped under ref, the workspace
// key generation the session seals under: inside the transaction, with no call to the key
// service, and recorded in the installation's key history. A writer making it at the same time
// keeps the other's. An environment being purged, or whose key was destroyed, gets no value, nor
// a new key: ErrKeyDestroyed.
func (s *Session) environmentSealingKey(ctx context.Context, q Fence, environment uuid.UUID, ref keyRef) ([]byte, int64, error) {
	ws := s.request.Workspace
	row, err := q.EnvironmentKeyForSealing(ctx, db.EnvironmentKeyForSealingParams{WorkspaceID: ws, EnvironmentID: environment})
	if errors.Is(err, pgx.ErrNoRows) {
		// Writers making the first key take turns: the second finds the first's.
		if err = q.LockEnvironmentKeyCreation(ctx, "environment:"+ws.String()+":"+environment.String()); err != nil {
			return nil, 0, err
		}
		row, err = q.EnvironmentKeyForSealing(ctx, db.EnvironmentKeyForSealingParams{WorkspaceID: ws, EnvironmentID: environment})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var refused bool
		refused, err = q.EnvironmentSealingRefused(ctx, db.EnvironmentSealingRefusedParams{WorkspaceID: ws, EnvironmentID: environment})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Not one of the workspace's environments.
			return nil, 0, ErrNotAllowed
		case err != nil:
			return nil, 0, err
		case refused:
			return nil, 0, ErrKeyDestroyed
		}
		if err = s.makeEnvironmentKey(ctx, q, environment, ref, 1); err != nil {
			return nil, 0, err
		}
		row, err = q.EnvironmentKeyForSealing(ctx, db.EnvironmentKeyForSealingParams{WorkspaceID: ws, EnvironmentID: environment})
	}
	if err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		return nil, 0, ErrNeedKeys
	}
	ref2 := environmentRef{environment, row.Generation}
	// A key made since the session began is held as this transaction stores it, never as an
	// earlier transaction that rolled back made it.
	s.holdProvisional(ref2, &environmentKey{workspaceGeneration: row.WorkspaceGeneration, wrapped: row.Wrapped, kcv: row.Kcv})
	key, err := s.openEnvironmentKey(ref2, s.environments[ref2])
	return key, row.Generation, err
}

// ReplaceLostEnvironmentKey gives an environment whose keys the restored database never had a key
// at the generation after lost, the newest one the hosts' checkpoints name, wrapped under the
// workspace key generation the session seals under. A key made later would otherwise take a lost
// key's generation, under which a value sealed with the lost key fails to open as if corrupt; above
// them, such a value stays one recovery stops for, ErrKeyNotInBackup. Only recovery calls it. An
// environment that already has a key keeps it, and one being purged gets none: ErrKeyDestroyed.
func (s *Session) ReplaceLostEnvironmentKey(ctx context.Context, q Fence, environment uuid.UUID, lost int64) error {
	if s.request.Purpose != Recover {
		return ErrNotAllowed
	}
	ws := s.request.Workspace
	ref, err := s.fence(ctx, q, Item{Kind: SecretVersion, ID: environment, Environment: environment})
	if err != nil {
		return err
	}
	if err = q.LockEnvironmentKeyCreation(ctx, "environment:"+ws.String()+":"+environment.String()); err != nil {
		return err
	}
	if _, err = q.EnvironmentKeyForSealing(ctx, db.EnvironmentKeyForSealingParams{WorkspaceID: ws, EnvironmentID: environment}); !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	refused, err := q.EnvironmentSealingRefused(ctx, db.EnvironmentSealingRefusedParams{WorkspaceID: ws, EnvironmentID: environment})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotAllowed
	case err != nil:
		return err
	case refused:
		return ErrKeyDestroyed
	}
	return s.makeEnvironmentKey(ctx, q, environment, ref, lost+1)
}

// makeEnvironmentKey makes an environment's key at generation, wrapped under ref, and records it.
func (s *Session) makeEnvironmentKey(ctx context.Context, q Fence, environment uuid.UUID, ref keyRef, generation int64) error {
	ws := s.request.Workspace
	key := make([]byte, 32)
	defer clear(key)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	s.mu.Lock()
	workspaceKey, ok := s.keys[ref]
	var wrapped []byte
	var err error
	if ok {
		wrapped, err = cryptobox.WrapEnvironmentKey(workspaceKey, ws, environment, generation, ref.generation, key)
	}
	s.mu.Unlock()
	if !ok {
		return ErrNeedKeys
	}
	if err != nil {
		return err
	}
	if err = q.InsertEnvironmentKey(ctx, db.InsertEnvironmentKeyParams{WorkspaceID: ws, EnvironmentID: environment, Generation: generation, WorkspaceGeneration: ref.generation, Wrapped: wrapped, Kcv: custody.KeyCheck(key)}); err != nil {
		return err
	}
	return s.broker.event(ctx, q, keyEvent{run: s.id, kind: "environment-key-created", scope: custody.ScopeWorkspace, workspace: &ws, generation: ref.generation, details: map[string]string{
		"environment": environment.String(), "generation": strconv.FormatInt(generation, 10),
	}})
}

// EnvironmentKeyGeneration is the generation of item's environment's key that ciphertext, one of
// item's values, is sealed under, read from the envelope without opening it, and false for a value
// under any other key. Recovery names by it the values a restored database has no key for. A value
// under another environment's key is not item's: cryptobox.ErrKey.
func (s *Session) EnvironmentKeyGeneration(item Item, ciphertext []byte) (int64, bool, error) {
	if _, _, err := s.allowed(item, s.rule.open); err != nil {
		return 0, false, err
	}
	header, err := cryptobox.ReadHeader(ciphertext)
	if err != nil {
		return 0, false, err
	}
	if header.Scope != cryptobox.ScopeEnvironment {
		return 0, false, nil
	}
	if !item.Kind.environmental() || header.Holder != item.Environment {
		return 0, false, cryptobox.ErrKey
	}
	return header.Generation, true, nil
}
