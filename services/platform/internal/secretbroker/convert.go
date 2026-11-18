package secretbroker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/recoveryrows"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// convertStage is the custody stage converting completed first: no stored value is sealed under
// the installation keyring any more, and no instance seals one. sealEnvironmentsStage is the one
// it completes now, once every environment's values are also under the environment's own key, and
// no instance seals one under its workspace's key.
const (
	convertStage          = 4
	sealEnvironmentsStage = 6
)

// convertBatch is how many values one of a conversion's transactions moves.
const convertBatch = 100

// convertLockAttempts is how many times recording the stage tries to hold the tables with sealed
// values, waiting a little longer each time, before it reports them busy.
const convertLockAttempts = 3

// ErrConversionBusy is a stage check that could not hold the tables with sealed values: writers
// kept them busy, and it gave up rather than make later writers queue behind it.
var ErrConversionBusy = errors.New("the tables with stored values stayed busy, so custody stage 4 was not recorded: run it again at a quieter time")

// convertRestarts is how many sessions in a row a holder may need without moving a value before
// conversion gives up on it: a generation that keeps changing is a rotation, which it does not race.
const convertRestarts = 3

// Conversion is what one `custody convert` run did.
type Conversion struct {
	Run uuid.UUID
	// Holders is how many holders it went through: the installation, and each workspace with a value
	// under the keyring, which alone get a session and an access record. Values is how many values it
	// moved; Raced how many it left because they changed while it ran.
	Holders, Values, Raced int64
	// Skipped names the holders it could not convert, and why.
	Skipped []string
	// Counts is, by kind, how many values are stored afterwards, and how many of them are still
	// sealed under the keyring. Recording the stage reads every one of them.
	Counts []db.LegacyValueCountsRow
	// Stage is the custody stage recorded afterwards, and Waiting why it is not convertStage.
	Stage   int32
	Waiting string
}

// Convert moves every stored value sealed under the installation keyring onto its holder's key:
// a workspace value onto its workspace key, a second factor onto the installation key. It keeps each
// value's data, so recovery data captured before still matches it by content: before moving a
// workspace's values it moves recovery's records of the rows it restored to their content form. It
// never touches recovery checkpoints, which hosts hold bound by digest. It runs for an operator, as one
// recorded run, and is safe to run again or beside `serve`: each holder is taken under the lock
// creating its key takes, and a value written while it runs is kept. Once nothing remains under the
// keyring and no running instance seals under it, it records custody stage 4. Like Begin, it must
// not run inside a transaction.
func (b *Broker) Convert(ctx context.Context, operator, reason string) (Conversion, error) {
	c := Conversion{Run: uuid.New()}
	if b.pool == nil {
		return c, errors.New("custody is not installed")
	}
	if operator == "" || len(operator) > 200 || len(reason) > 1000 {
		return c, errors.New("an operator name of at most 200 characters is required, and a reason is at most 1000")
	}
	if err := b.event(ctx, db.New(b.pool), keyEvent{run: c.Run, kind: "convert-started", operator: operator, reason: reason}); err != nil {
		return c, err
	}
	err := b.convert(ctx, &c)
	details := map[string]string{"holders": strconv.FormatInt(c.Holders, 10), "values": strconv.FormatInt(c.Values, 10), "raced": strconv.FormatInt(c.Raced, 10), "skipped": strconv.Itoa(len(c.Skipped))}
	var remaining int64
	for _, r := range c.Counts {
		remaining += r.Remaining
	}
	details["remaining"] = strconv.FormatInt(remaining, 10)
	b.finish(ctx, c.Run, "convert-finished", Done(err), details)
	return c, err
}

func (b *Broker) convert(ctx context.Context, c *Conversion) error {
	if err := b.convertHolder(ctx, c, uuid.Nil); err != nil {
		return err
	}
	q := db.New(b.pool)
	var after uuid.UUID
	for {
		ids, err := q.ConversionWorkspaces(ctx, db.ConversionWorkspacesParams{After: after, Batch: initBatch})
		if err != nil {
			return err
		}
		for _, ws := range ids {
			if err = b.convertHolder(ctx, c, ws); err != nil {
				return err
			}
			after = ws
		}
		if len(ids) < initBatch {
			break
		}
	}
	var err error
	if c.Counts, err = q.LegacyValueCounts(ctx); err != nil {
		return err
	}
	sealable, err := sealsUnderEnvironments(ctx, q)
	if err != nil {
		return err
	}
	var remaining, outside int64
	for _, r := range c.Counts {
		remaining += r.Remaining
		outside += r.Outside
	}
	switch {
	case len(c.Skipped) > 0:
		c.Waiting = fmt.Sprintf("%d holders were not converted", len(c.Skipped))
	case remaining > 0:
		c.Waiting = fmt.Sprintf("%d values are still sealed under the installation keyring: run this again", remaining)
	case outside > 0 && !sealable:
		c.Waiting = fmt.Sprintf("%d environment values stay under their workspace's key while an instance of a release before stage 5 runs: run this again five minutes after the last one stopped", outside)
	case outside > 0:
		c.Waiting = fmt.Sprintf("%d environment values are not yet under their environment's key: run this again", outside)
	default:
		c.Stage, c.Waiting, err = b.completeConversion(ctx)
		return err
	}
	c.Stage, err = q.CustodyStage(ctx)
	return err
}

// convertHolder moves one holder's values, in batches, each in a transaction that holds the
// generation it seals under, as storing a new value does. A holder another run or the key job holds
// is skipped, as is one with no key to move onto yet.
func (b *Broker) convertHolder(ctx context.Context, c *Conversion, holder uuid.UUID) error {
	scope := custody.ScopeWorkspace
	if holder == uuid.Nil {
		scope = custody.ScopeInstallation
	}
	name := string(scope) + ":" + holder.String()
	if holder != uuid.Nil {
		// Most workspaces have nothing under the keyring: look before locking it or opening its
		// keys. One with nothing yet that gains a value is left for the next run, as the counts
		// afterwards say.
		q := db.New(b.pool)
		sealable, err := sealsUnderEnvironments(ctx, q)
		if err != nil {
			return err
		}
		values, _, _, err := legacyValues(ctx, q, holder, convertCursor{}, sealable)
		if err != nil || len(values) == 0 {
			return err
		}
	}
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	locked, err := db.New(conn).TryLockKeyCreation(ctx, name)
	if err != nil {
		return err
	}
	if !locked {
		c.Skipped = append(c.Skipped, name+": another run holds it")
		return nil
	}
	defer func() {
		if _, err := db.New(conn).UnlockKeyCreation(context.WithoutCancel(ctx), name); err != nil {
			// A lock that cannot be released goes with the connection.
			b.log.Warn("key holder lock not released; closing its connection", "holder", name, "error", err.Error())
			conn.Hijack().Close(context.WithoutCancel(ctx))
		}
	}()
	c.Holders++
	if holder != uuid.Nil {
		// Recovery's record of the rows it restored must name them by content before their
		// values move, or a checkpoint they came from stops merging.
		if err = rekeyRestoredRows(ctx, conn, holder); err != nil {
			return err
		}
	}
	kinds := slices.DeleteFunc(slices.Clone(workspaceKinds), func(k Kind) bool { return k == RecoveryCheckpoint })
	if holder == uuid.Nil {
		kinds = slices.Clone(installationKinds)
	}
	r := Request{Workspace: holder, Purpose: Rotate, Principal: Operator(c.Run.String()), Kinds: kinds, Reason: "convert"}
	var session *Session
	var moved int64
	end := func(err error) {
		if session != nil {
			session.Close(ctx, UsedIf(moved > 0 || err == nil))
			session = nil
		}
	}
	cursor := convertCursor{}
	restarts := 0
	for {
		if session == nil {
			// An operator's command is admitted by the purpose table alone: it acts for every holder.
			if session, err = b.begin(ctx, r, rules[Rotate]); err != nil {
				return err
			}
		}
		batch, err := b.convertBatch(ctx, conn, c.Run, session, holder, &cursor)
		moved += batch.values
		c.Values += batch.values
		c.Raced += batch.raced
		switch {
		case errors.Is(err, ErrKeyPending) || errors.Is(err, ErrNoSealableKey):
			end(err)
			c.Skipped = append(c.Skipped, name+": "+err.Error())
			return nil
		case errors.Is(err, ErrNeedKeys):
			// The session ended, or its generation stopped being the one to seal under: begin
			// again from where this batch started.
			end(err)
			if restarts++; restarts >= convertRestarts {
				return fmt.Errorf("%s: its key generation kept changing: %w", name, err)
			}
			continue
		case err != nil:
			end(err)
			return err
		}
		restarts = 0
		if batch.done {
			end(nil)
			return nil
		}
	}
}

// rekeyRestoredRows moves a workspace's records of restored rows to their content form, in its own
// transaction on the connection holding the workspace's lock.
func rekeyRestoredRows(ctx context.Context, conn pgxConn, ws uuid.UUID) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// A rewrap moves the same records, in its own transaction: wait for it rather than deadlock.
	if err = db.New(tx).LockBootstrap(ctx); err != nil {
		return err
	}
	if _, err = recoveryrows.Rekey(ctx, tx, ws); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// convertCursor is where a holder's conversion has got to: the last value it read, by kind and ID.
type convertCursor struct {
	kind string
	id   uuid.UUID
}

type convertResult struct {
	values, raced int64
	done          bool
}

// convertBatch moves the next batch of a holder's values in one transaction, and records how many
// it moved in the same one, so the record exists exactly when they do. The cursor moves only
// once the transaction commits.
func (b *Broker) convertBatch(ctx context.Context, conn pgxConn, run uuid.UUID, session *Session, holder uuid.UUID, cursor *convertCursor) (convertResult, error) {
	var out convertResult
	tx, err := conn.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	// A rewrap moves values installation-wide in one transaction, locking them in another order:
	// wait for it rather than deadlock with it.
	if err = q.LockBootstrap(ctx); err != nil {
		return out, err
	}
	sealable, err := sealsUnderEnvironments(ctx, q)
	if err != nil {
		return out, err
	}
	values, next, full, err := legacyValues(ctx, q, holder, *cursor, sealable)
	if err != nil {
		return out, err
	}
	for _, v := range values {
		if header, err := cryptobox.ReadHeader(v.sealed); err == nil && header.Version == 2 && (v.item.Environment == uuid.Nil || header.Scope == cryptobox.ScopeEnvironment) {
			// The database reads only the envelope's first bytes; this one is converted already.
			continue
		}
		converted, err := session.Rehome(ctx, q, v.item, v.sealed)
		if errors.Is(err, ErrKeyDestroyed) {
			// Its environment was purged since it was read: the purge erased it.
			out.raced++
			continue
		}
		if err != nil {
			return convertResult{}, convertFailed(v.item, holder, err)
		}
		n, err := v.store(ctx, q, converted)
		if err != nil {
			return convertResult{}, err
		}
		if n == 0 {
			// It changed since it was read: what replaced it is kept, and a later run converts
			// it if it needs to be.
			out.raced++
		} else {
			out.values++
		}
	}
	if out.values > 0 || out.raced > 0 {
		e := keyEvent{run: run, kind: "values-converted", scope: custody.ScopeInstallation, details: map[string]string{"values": strconv.FormatInt(out.values, 10), "raced": strconv.FormatInt(out.raced, 10)}}
		if holder != uuid.Nil {
			e.scope, e.workspace = custody.ScopeWorkspace, &holder
		}
		if err = b.event(ctx, q, e); err != nil {
			return convertResult{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return convertResult{}, err
	}
	*cursor = next
	out.done = !full
	return out, nil
}

// pgxConn is the connection a holder's lock is held on, which its batches run on too.
type pgxConn interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// convertFailed names the value that could not move, so an operator can find it, without its
// contents. The session's errors keep their meaning for the caller.
func convertFailed(item Item, holder uuid.UUID, err error) error {
	if errors.Is(err, ErrNeedKeys) || errors.Is(err, ErrKeyPending) || errors.Is(err, ErrNoSealableKey) {
		return err
	}
	if holder == uuid.Nil {
		return fmt.Errorf("cannot convert the %s value of account %s: %w", item.Kind, item.ID, err)
	}
	return fmt.Errorf("cannot convert %s %s in workspace %s: %w", item.Kind, item.ID, holder, err)
}

// legacyValue is one value still sealed under the keyring, and how to store it converted.
type legacyValue struct {
	item   Item
	sealed []byte
	store  func(ctx context.Context, q *db.Queries, converted []byte) (int64, error)
}

// legacyValues reads the next batch of a holder's values sealed under the keyring, after cursor,
// and says where the next batch starts and whether this one was full.
func legacyValues(ctx context.Context, q *db.Queries, holder uuid.UUID, cursor convertCursor, environments bool) ([]legacyValue, convertCursor, bool, error) {
	if holder == uuid.Nil {
		return legacySecondFactors(ctx, q, cursor)
	}
	rows, err := q.LegacyWorkspaceValues(ctx, db.LegacyWorkspaceValuesParams{Workspace: holder, Environments: environments, AfterKind: cursor.kind, AfterID: cursor.id, Batch: convertBatch})
	if err != nil {
		return nil, cursor, false, err
	}
	out := make([]legacyValue, 0, len(rows))
	for _, row := range rows {
		ws, id, previous := holder, row.ID, row.Sealed
		// An environment's value moves onto the environment's own key, once every instance opens
		// values under one.
		v := legacyValue{item: Item{Kind: Kind(row.Kind), ID: id, Environment: row.EnvironmentID}, sealed: previous}
		switch v.item.Kind {
		case SecretVersion:
			v.store = func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertSecretVersion(ctx, db.ConvertSecretVersionParams{Converted: converted, WorkspaceID: ws, ID: id, Previous: previous})
			}
		case Git:
			v.store = func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertGitAccess(ctx, db.ConvertGitAccessParams{Converted: converted, WorkspaceID: ws, ID: id, Previous: previous})
			}
		case GitHubAuthorization:
			v.store = func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertGitHubAuthorization(ctx, db.ConvertGitHubAuthorizationParams{Converted: converted, WorkspaceID: ws, ID: id, Previous: previous})
			}
		case Registry:
			v.store = func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertRegistryAccess(ctx, db.ConvertRegistryAccessParams{Converted: converted, WorkspaceID: ws, ID: id, Previous: previous})
			}
		case BuildSecret:
			v.store = func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertBuildSecret(ctx, db.ConvertBuildSecretParams{Converted: converted, WorkspaceID: ws, ID: id, Previous: previous})
			}
		case BackupDestination:
			v.store = func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertBackupDestinationAccess(ctx, db.ConvertBackupDestinationAccessParams{Converted: converted, WorkspaceID: ws, ID: id, Previous: previous})
			}
		case BackupPolicy:
			v.store = func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertBackupPolicyAccess(ctx, db.ConvertBackupPolicyAccessParams{Converted: converted, WorkspaceID: ws, ID: id, Previous: previous})
			}
		case AlertWebhook:
			v.store = func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertAlertWebhook(ctx, db.ConvertAlertWebhookParams{Converted: converted, WorkspaceID: ws, Previous: previous})
			}
		default:
			return nil, cursor, false, fmt.Errorf("a %s value has no conversion", row.Kind)
		}
		out = append(out, v)
		cursor = convertCursor{row.Kind, id}
	}
	return out, cursor, len(rows) == convertBatch, nil
}

// legacySecondFactors reads the next batch of accounts with a second factor, active or pending,
// sealed under the keyring. The cursor is the last account read.
func legacySecondFactors(ctx context.Context, q *db.Queries, cursor convertCursor) ([]legacyValue, convertCursor, bool, error) {
	rows, err := q.LegacyTOTPSecrets(ctx, db.LegacyTOTPSecretsParams{After: cursor.id, Batch: convertBatch})
	if err != nil {
		return nil, cursor, false, err
	}
	var out []legacyValue
	for _, row := range rows {
		id := row.ID
		if active := row.TotpSecret; active != nil {
			out = append(out, legacyValue{item: Item{Kind: TOTP, ID: id}, sealed: active, store: func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertTOTPSecret(ctx, db.ConvertTOTPSecretParams{Converted: converted, ID: id, Previous: active})
			}})
		}
		if pending := row.TotpPending; pending != nil {
			out = append(out, legacyValue{item: Item{Kind: TOTPPending, ID: id}, sealed: pending, store: func(ctx context.Context, q *db.Queries, converted []byte) (int64, error) {
				return q.ConvertTOTPPending(ctx, db.ConvertTOTPPendingParams{Converted: converted, ID: id, Previous: pending})
			}})
		}
		cursor = convertCursor{id: id}
	}
	return out, cursor, len(rows) == convertBatch, nil
}

// completeConversion records custody stage 4 once no value is sealed under the keyring and no
// instance seen lately runs a release that seals under it. It reports the stage recorded, and
// otherwise why not. Holding the tables waits at most a few seconds for each, so writers do not
// queue behind it: it lets go and tries again, a few times, then reports ErrConversionBusy.
func (b *Broker) completeConversion(ctx context.Context) (int32, string, error) {
	for attempt := 1; ; attempt++ {
		stage, waiting, err := b.recordConverted(ctx)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			return stage, waiting, err
		}
		if attempt == convertLockAttempts {
			return 0, "", fmt.Errorf("%w: %w", ErrConversionBusy, err)
		}
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
}

// recordConverted is one attempt at completeConversion, in one transaction.
func (b *Broker) recordConverted(ctx context.Context) (int32, string, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	counts, err := b.holdSealedValues(ctx, q)
	if err != nil {
		// The transaction failed with it, so the stage is read by whoever reports it.
		return 0, "", err
	}
	waiting, err := b.keyringWaiting(ctx, q, counts)
	if err == nil && waiting == "" {
		waiting, err = b.environmentsWaiting(ctx, q, counts)
	}
	if err != nil {
		return 0, "", err
	}
	if waiting != "" {
		stage, err := q.CustodyStage(ctx)
		return stage, waiting, err
	}
	n, err := q.AdvanceCustodyStage(ctx, sealEnvironmentsStage-1)
	if err != nil {
		return 0, "", err
	}
	if n == 1 {
		if err = b.event(ctx, q, keyEvent{run: uuid.New(), kind: "stage-completed", details: map[string]string{"stage": strconv.Itoa(sealEnvironmentsStage)}}); err != nil {
			return 0, "", err
		}
	}
	stage, err := q.CustodyStage(ctx)
	if err != nil {
		return 0, "", err
	}
	if stage < sealEnvironmentsStage {
		return stage, "", fmt.Errorf("custody stage %d is not complete, so stage %d cannot be", sealEnvironmentsStage-1, sealEnvironmentsStage)
	}
	return stage, "", tx.Commit(ctx)
}

// CheckConverted holds every table with sealed values against writes until q's transaction ends,
// then reports why values may still be sealed under the installation keyring: some are, or an
// instance seen in the last five minutes runs a release that seals under it. Nothing can be stored
// between the check and the end of the transaction, so a stage recorded in it is true when it is
// recorded. An instance of an older release that starts later, or one unseen for longer, can still
// store such a value afterwards, so whatever stops reading the keyring runs this again first.
func (b *Broker) CheckConverted(ctx context.Context, q *db.Queries) (string, error) {
	counts, err := b.holdSealedValues(ctx, q)
	if err != nil {
		return "", err
	}
	return b.keyringWaiting(ctx, q, counts)
}

// holdSealedValues holds every table with sealed values against writes until q's transaction
// ends, and counts what they store.
func (b *Broker) holdSealedValues(ctx context.Context, q *db.Queries) ([]db.LegacyValueCountsRow, error) {
	// A rewrap holds these tables' rows for its whole transaction: wait for it before holding any.
	// Only rewraps, conversions and the first owner's bootstrap wait behind this lock, none of them
	// for long, so the wait is not bounded.
	if err := q.LockBootstrap(ctx); err != nil {
		return nil, err
	}
	if err := q.LimitLockWait(ctx, "2s"); err != nil {
		return nil, err
	}
	if err := q.HoldSealedValueTables(ctx); err != nil {
		return nil, fmt.Errorf("cannot hold the tables with sealed values against writes; try again: %w", err)
	}
	return q.LegacyValueCounts(ctx)
}

// keyringWaiting says why values may still be sealed under the installation keyring: some are, or
// an instance seen in the last five minutes runs a release that seals under it.
func (b *Broker) keyringWaiting(ctx context.Context, q *db.Queries, counts []db.LegacyValueCountsRow) (string, error) {
	var remaining int64
	for _, c := range counts {
		remaining += c.Remaining
	}
	if remaining > 0 {
		return fmt.Sprintf("%d values are still sealed under the installation keyring", remaining), nil
	}
	older, err := q.InstancesBelowStage(ctx, sealStage)
	if err != nil {
		return "", err
	}
	if older > 0 {
		return fmt.Sprintf("%d instances seen in the last five minutes run a release before stage 4, which may store values under the installation keyring: upgrade any still running, then run this again five minutes after the last one stopped", older), nil
	}
	return "", nil
}

// environmentsWaiting says why an environment's values may still be under its workspace's key:
// some are, or an instance seen in the last five minutes runs a release that seals them there.
func (b *Broker) environmentsWaiting(ctx context.Context, q *db.Queries, counts []db.LegacyValueCountsRow) (string, error) {
	var outside int64
	for _, c := range counts {
		outside += c.Outside
	}
	if outside > 0 {
		return fmt.Sprintf("%d environment values are not under their environment's key", outside), nil
	}
	older, err := q.InstancesBelowStage(ctx, sealEnvironmentsStage)
	if err != nil {
		return "", err
	}
	if older > 0 {
		return fmt.Sprintf("%d instances seen in the last five minutes run a release before stage %d, which seals environment values under their workspace's key: upgrade any still running, then run this again five minutes after the last one stopped", older, sealEnvironmentsStage), nil
	}
	return "", nil
}
