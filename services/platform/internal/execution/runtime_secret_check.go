package execution

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	// secretCheckRecent is how many of each secret's newest versions are searched for, whatever
	// names them: the current one and the two it replaced, which may still be valid where they
	// were issued. Older versions are searched for only while something that may deploy names them.
	secretCheckRecent = 3
	// secretCheckVersions and secretCheckBytes bound the check's work. Searching for one version
	// in crafted text costs up to about half a millisecond, so the count bounds it; the bytes
	// bound what is opened and held.
	secretCheckVersions = 256
	secretCheckBytes    = 1 << 20
	// secretValueLimit is the longest value a secret version holds.
	secretValueLimit = 65536
)

// secretCheckBudget fails the check closed: values it cannot search for might be in the text.
var secretCheckBudget = &domain.Error{Code: "invalid_input", Message: "The environment's runtime secrets exceed what can be checked against these files. An owner can retire secrets it no longer needs, or replace versions that the draft or the deployment still uses.", Fields: []string{"source.secretBudget"}}

// secretExposed refuses text that holds a runtime secret's value. It names no secret or file.
var secretExposed = &domain.Error{Code: "invalid_input", Message: "A file's name or contents include a known runtime secret value.", Fields: []string{"source.containsRuntimeSecret"}}

// checkedVersion is a version the check searches for. Required ones must be searched for or the
// check fails; the rest fill what the budget leaves.
type checkedVersion struct {
	id       uuid.UUID
	required bool
}

// chooseCheckedVersions picks what the check searches for from candidates, which are ordered by
// secret and then newest first. Each secret's current version, and every version that something
// that may still deploy names, is required, and they must fit the budget. A value too short to
// search for is never chosen. Superseded versions
// among each secret's newest fill what is left, most recently replaced first, so rotating a
// secret never pushes the environment past the budget. A superseded version whose length is not
// yet measured counts at the most a value can hold, so opening it never crosses the budget.
func chooseCheckedVersions(candidates []db.RuntimeSecretCheckCandidatesRow) ([]checkedVersion, error) {
	var chosen []checkedVersion
	var superseded []db.RuntimeSecretCheckCandidatesRow
	bytes := 0
	for _, c := range candidates {
		if c.PlaintextBytes.Valid && domain.SecretTooShortToSearch(int(c.PlaintextBytes.Int32)) {
			continue
		}
		if !c.Current && !c.Named {
			superseded = append(superseded, c)
			continue
		}
		// An unmeasured required value is counted when it is opened.
		if c.PlaintextBytes.Valid {
			bytes += int(c.PlaintextBytes.Int32)
		}
		chosen = append(chosen, checkedVersion{id: c.ID, required: true})
	}
	if len(chosen) > secretCheckVersions || bytes > secretCheckBytes {
		return nil, secretCheckBudget
	}
	// Each secret's last replaced version before any secret's older ones.
	slices.SortStableFunc(superseded, func(a, b db.RuntimeSecretCheckCandidatesRow) int { return int(a.Recency - b.Recency) })
	for _, c := range superseded {
		size := secretValueLimit
		if c.PlaintextBytes.Valid {
			size = int(c.PlaintextBytes.Int32)
		}
		if len(chosen) == secretCheckVersions || bytes+size > secretCheckBytes {
			continue
		}
		bytes += size
		chosen = append(chosen, checkedVersion{id: c.ID})
	}
	return chosen, nil
}

// rejectRuntimeSecrets refuses text in bundle that holds a value of the environment's runtime
// secrets. Choosing the versions reads only stored lengths, under the caller's locks. Opening and
// searching them runs between passes, with nothing held, and its answer is kept for the versions
// it searched: versions never change, so a later pass that chooses the same ones under its own
// locks takes the answer without opening anything again. An environment the pass created is
// searched in the pass instead.
func (s *Service) rejectRuntimeSecrets(ctx context.Context, tx pgx.Tx, git *gitAnswers, ws, env uuid.UUID, bundle composeproject.Bundle) error {
	q := db.New(tx)
	// A submission pushes both: the path names a tree entry, the content fills its blob.
	texts := make([]string, 0, 2*len(bundle.Files))
	for _, file := range bundle.Files {
		texts = append(texts, file.Path, file.Content)
	}
	// Empty text exposes nothing, so nothing is opened for it.
	if !slices.ContainsFunc(texts, func(text string) bool { return text != "" }) {
		return nil
	}
	// A plan not yet applied is applied only while the draft is still the revision it was made from
	// (applyVisualPlan), so the draft names its versions. Reading pending plans would read every
	// plan the environment has made.
	used, err := q.EnvironmentSecretVersionsInUse(ctx, db.EnvironmentSecretVersionsInUseParams{WorkspaceID: ws, EnvironmentID: env, PendingPlans: false})
	if err != nil {
		return err
	}
	named := make([]uuid.UUID, len(used))
	for i, v := range used {
		named[i] = v.ID
	}
	candidates, err := q.RuntimeSecretCheckCandidates(ctx, db.RuntimeSecretCheckCandidatesParams{WorkspaceID: ws, EnvironmentID: env, Named: named, Recent: secretCheckRecent})
	if err != nil {
		return err
	}
	chosen, err := chooseCheckedVersions(candidates)
	if err != nil || len(chosen) == 0 {
		return err
	}
	// An environment this pass created, such as a new preview's, holds copies its transaction
	// wrote: they can't be read between passes, and the next pass writes them again under new
	// IDs, so they are searched here. They are no more than a deployment's secrets.
	if git.createdEnvironments[env] {
		ids := make([]uuid.UUID, len(chosen))
		for i, c := range chosen {
			ids[i] = c.id
		}
		rows, err := q.SecretVersionValues(ctx, db.SecretVersionValuesParams{WorkspaceID: ws, EnvironmentID: env, Ids: ids})
		if err != nil {
			return err
		}
		return searchSecretValues(domain.NewSecretText(texts...), chosen, openChosen(rows, func(item secretbroker.Item, cipher []byte) ([]byte, error) {
			return git.keys.inspect.open(item, cipher)
		}))
	}
	key := secretCheckKey(env, texts, chosen)
	if answer, ok := git.secretCheckAnswer(key); ok {
		return answer
	}
	return &providerCall{fetch: func(ctx context.Context) {
		if _, ok := git.secretChecks[key]; !ok {
			git.secretChecks[key] = s.searchRuntimeSecrets(ctx, git.keys.inspect, ws, env, texts, chosen)
		}
	}}
}

// secretCheckAnswer returns the kept answer for exactly the search key names. An answer is kept
// only for the texts and versions it searched, so one chosen differently finds none and is
// searched again. A broker refusal is returned once and not kept; a search that missed a chosen
// version is no answer at all.
func (g *gitAnswers) secretCheckAnswer(key [32]byte) (error, bool) {
	answer, ok := g.secretChecks[key]
	if !ok {
		return nil, false
	}
	var refused *keysUnavailable
	if errors.As(answer, &refused) {
		delete(g.secretChecks, key)
		return refused.err, true
	}
	if errors.Is(answer, errCheckedVersionsChanged) {
		delete(g.secretChecks, key)
		return nil, false
	}
	return answer, true
}

// errCheckedVersionsChanged is a search that could not read every version chosen, such as one
// erased meanwhile. The pass that finds it chooses and searches again.
var errCheckedVersionsChanged = errors.New("the versions chosen for the runtime-secret check changed")

// searchRuntimeSecrets opens the chosen versions and searches texts for each. It runs with no
// transaction open. Its answer covers exactly the versions chosen, or it is
// errCheckedVersionsChanged.
func (s *Service) searchRuntimeSecrets(ctx context.Context, keys valueKeys, ws, env uuid.UUID, texts []string, chosen []checkedVersion) error {
	ids := make([]uuid.UUID, len(chosen))
	for i, c := range chosen {
		ids[i] = c.id
	}
	rows, err := db.New(s.Store.Pool).SecretVersionValues(ctx, db.SecretVersionValuesParams{WorkspaceID: ws, EnvironmentID: env, Ids: ids})
	if err != nil {
		return err
	}
	return searchSecretValues(domain.NewSecretText(texts...), chosen, openChosen(rows, func(item secretbroker.Item, cipher []byte) ([]byte, error) {
		return keys.openBetweenPasses(ctx, item, cipher)
	}))
}

// openChosen opens a chosen version from rows with open; one missing from rows was erased since
// it was chosen.
func openChosen(rows []db.SecretVersionValuesRow, open func(secretbroker.Item, []byte) ([]byte, error)) func(checkedVersion) ([]byte, error) {
	ciphers := make(map[uuid.UUID][]byte, len(rows))
	for _, row := range rows {
		ciphers[row.ID] = row.EncryptedValue
	}
	return func(c checkedVersion) ([]byte, error) {
		cipher, ok := ciphers[c.id]
		if !ok {
			return nil, errCheckedVersionsChanged
		}
		return open(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: c.id}, cipher)
	}
}

// searchSecretValues searches for each chosen version's value, opened by open, in order.
func searchSecretValues(searched domain.SecretText, chosen []checkedVersion, open func(checkedVersion) ([]byte, error)) error {
	total := 0
	for _, c := range chosen {
		value, err := open(c)
		if err != nil {
			return err
		}
		// A value the measuring job has not reached may turn out too short to search for.
		if domain.SecretTooShortToSearch(len(value)) {
			clear(value)
			continue
		}
		// The required values come first. One the measuring job has not reached can leave less room
		// than the choice assumed, and then a superseded value that no longer fits is skipped, as
		// it would have been when choosing.
		if total+len(value) > secretCheckBytes {
			clear(value)
			if c.required {
				return secretCheckBudget
			}
			continue
		}
		total += len(value)
		exposed := searched.Exposes(string(value))
		clear(value)
		if exposed {
			return secretExposed
		}
	}
	return nil
}

// secretCheckKey identifies one search: the environment, the texts and the versions chosen. The
// texts' order does not change the search, and callers may collect them from a map, so it does
// not change the key either.
func secretCheckKey(env uuid.UUID, texts []string, chosen []checkedVersion) [32]byte {
	h := sha256.New()
	h.Write(env[:])
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(texts)))
	h.Write(n[:])
	for _, text := range slices.Sorted(slices.Values(texts)) {
		binary.BigEndian.PutUint64(n[:], uint64(len(text)))
		h.Write(n[:])
		h.Write([]byte(text))
	}
	for _, c := range chosen {
		h.Write(c.id[:])
		if c.required {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	}
	return [32]byte(h.Sum(nil))
}
