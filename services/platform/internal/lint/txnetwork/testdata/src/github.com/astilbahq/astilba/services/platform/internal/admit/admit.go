package admit

import (
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/jackc/pgx/v5"
)

type service struct {
	provider *pipeline.Provider
	github   *githubapp.Client
	queries  *db.Queries
}

func scope(fn func(pgx.Tx) error) error { return nil }

// The shape of the original defect: a provider call inside the locking scope's body.
func (s *service) underScope() error {
	return scope(func(tx pgx.Tx) error {
		_, err := s.provider.Resolve("main") // want `contacts a Git provider`
		return err
	})
}

func (s *service) withTransactionParameter(tx pgx.Tx) error {
	_, err := s.github.Mint() // want `contacts a Git provider`
	return err
}

func (s *service) withQueriesParameter(q *db.Queries) error {
	_, err := s.provider.Resolve("main") // want `contacts a Git provider`
	return err
}

func (s *service) token() (string, error) { return s.github.Mint() }
func (s *service) indirect() (string, error) {
	return s.token()
}

// A helper hides the call from its caller, however many functions deep.
func (s *service) throughHelpers(tx pgx.Tx) error {
	_, err := s.indirect() // want `contacts a Git provider`
	return err
}

func (s *service) localOnly(tx pgx.Tx) string { return s.github.InstallURL() }

// The correct shape: the call is a closure, made once the transaction has gone.
func (s *service) betweenPasses(ask func(func() error)) error {
	return scope(func(tx pgx.Tx) error {
		ask(func() error {
			_, err := s.provider.Resolve("main")
			return err
		})
		return nil
	})
}

// A call before the transaction opens is the correct shape too.
func (s *service) beforeOpening(begin func() (pgx.Tx, error)) error {
	if _, err := s.provider.Resolve("main"); err != nil {
		return err
	}
	tx, err := begin()
	if err != nil {
		return err
	}
	return tx.Commit()
}

// The pool-backed queries a service keeps hold nothing between statements.
func (s *service) outsideTransactions() error {
	if err := s.queries.Read(); err != nil {
		return err
	}
	_, err := s.provider.Resolve("main")
	return err
}

// The shape of the original defect in a worker: a local transaction, then the call.
func (s *service) afterOpening(begin func() (pgx.Tx, error)) error {
	tx, err := begin()
	if err != nil {
		return err
	}
	if _, err = s.provider.Resolve("main"); err != nil { // want `contacts a Git provider`
		return err
	}
	return tx.Commit()
}

func (s *service) throughQueriesLocal(begin func() (pgx.Tx, error), wrap func(pgx.Tx) *db.Queries) error {
	tx, err := begin()
	if err != nil {
		return err
	}
	q := wrap(tx)
	_, err = s.github.Mint() // want `contacts a Git provider`
	if err != nil {
		return err
	}
	return q.Read()
}

// Declaring a transaction holds nothing until it is opened.
func (s *service) declaredFirst(begin func() (pgx.Tx, error)) error {
	var tx pgx.Tx
	if _, err := s.provider.Resolve("main"); err != nil {
		return err
	}
	tx, err := begin()
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *service) namesAFunctionType() error {
	var body func(tx pgx.Tx) error
	_ = body
	_, err := s.provider.Resolve("main")
	return err
}

func (s *service) askThenOpen() (pgx.Tx, error) {
	_, err := s.provider.Resolve("main")
	return nil, err
}

// A helper that opens the transaction after asking has not asked with one open.
func (s *service) openedByTheAskingHelper() error {
	tx, err := s.askThenOpen()
	if err != nil {
		return err
	}
	return tx.Commit()
}

// A literal called on the spot or deferred runs with the transaction open.
func (s *service) calledOnTheSpot(tx pgx.Tx) error {
	func() {
		_, _ = s.provider.Resolve("main") // want `contacts a Git provider`
	}()
	defer func() {
		_, _ = s.github.Mint() // want `contacts a Git provider`
	}()
	return tx.Commit()
}

// Naming the transaction as well, such a literal is still one report for each call.
func (s *service) onTheSpotNamingIt(tx pgx.Tx) {
	func() {
		_ = tx.Commit()
		_, _ = s.provider.Resolve("main") // want `contacts a Git provider`
	}()
}

// A literal that captures the transaction has it from its first line.
func (s *service) captured(begin func() (pgx.Tx, error), run func(func() error) error) error {
	tx, err := begin()
	if err != nil {
		return err
	}
	return run(func() error {
		_, err := s.provider.Resolve("main") // want `contacts a Git provider`
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

// A savepoint opened later doesn't move where the literal first had the transaction.
func (s *service) capturedBeforeASavepoint(outer pgx.Tx, begin func(pgx.Tx) (pgx.Tx, error), run func(func() error) error) error {
	return run(func() error {
		if _, err := s.provider.Resolve("main"); err != nil { // want `contacts a Git provider`
			return err
		}
		nested, err := begin(outer)
		if err != nil {
			return err
		}
		return nested.Commit()
	})
}

func generic[T any](s *service, value T) (T, error) {
	_, err := s.github.Mint()
	return value, err
}

func twoTypes[A, B any](s *service, value A, _ B) (A, error) {
	_, err := s.provider.Resolve("main")
	return value, err
}

func (s *service) throughGenerics(tx pgx.Tx) {
	_, _ = generic[string](s, "")          // want `contacts a Git provider`
	_, _ = generic(s, "")                  // want `contacts a Git provider`
	_, _ = twoTypes[string, int](s, "", 0) // want `contacts a Git provider`
}

type box[T any] struct{ service *service }

func (b *box[T]) fetch() (string, error) { return b.service.github.Mint() }

func (s *service) throughAGenericType(tx pgx.Tx) {
	b := &box[int]{s}
	_, _ = b.fetch() // want `contacts a Git provider`
}

func (s *service) first() (string, error)  { return s.second() }
func (s *service) second() (string, error) { return s.third() }
func (s *service) third() (string, error) {
	if s.reach() == "" {
		return s.first()
	}
	return "", nil
}
func (s *service) reach() string {
	token, _ := s.github.Mint()
	return token
}

// However long the chain, and even through recursion, the helper reaches the network.
func (s *service) throughAChain(tx pgx.Tx) {
	_, _ = s.first() // want `contacts a Git provider`
}

type aliased = pgx.Tx

func (s *service) withAnAlias(tx aliased) {
	_, _ = s.provider.Resolve("main") // want `contacts a Git provider`
}

// Each of these only computes or checks a value.
func (s *service) offline(tx pgx.Tx) {
	_ = s.github.FixtureAPI()
	_ = s.github.InstallURL()
	_ = s.github.AuthorizeURL()
	_ = s.github.VerifyWebhook()
}

// A transaction scoped to a literal has gone by the time the call runs.
func (s *service) afterTheScope() error {
	if err := scope(func(tx pgx.Tx) error { return tx.Commit() }); err != nil {
		return err
	}
	_, err := s.provider.Resolve("main")
	return err
}

type transactionBody func(tx pgx.Tx) error

func (s *service) namesATypeOfFunction() error {
	type local func(tx pgx.Tx) error
	var _ local = func(pgx.Tx) error { return nil }
	var _ transactionBody
	_, err := s.provider.Resolve("main")
	return err
}

// Queries a function keeps locally may be the pool's, which hold nothing between statements.
func (s *service) poolQueriesLocal() error {
	q := s.queries
	if err := q.Read(); err != nil {
		return err
	}
	_, err := s.provider.Resolve("main")
	return err
}

type holder struct{ tx pgx.Tx }

func (s *service) notLookedIntoAStruct(h holder) error {
	if err := h.tx.Commit(); err != nil {
		return err
	}
	_, err := s.provider.Resolve("main")
	return err
}

// A literal capturing the queries of a transaction has that transaction.
func (s *service) capturesQueries(q *db.Queries, run func(func() error) error) error {
	return run(func() error {
		if err := q.Read(); err != nil {
			return err
		}
		_, err := s.provider.Resolve("main") // want `contacts a Git provider`
		return err
	})
}

// A named result holds nothing until it is assigned.
func (s *service) namedResult(begin func() (pgx.Tx, error)) (tx pgx.Tx, err error) {
	if _, err = s.provider.Resolve("main"); err != nil {
		return nil, err
	}
	tx, err = begin()
	if err != nil {
		return nil, err
	}
	return tx, nil
}

func (s *service) declaredWithAValue() error {
	var tx, err = s.askThenOpen()
	if err != nil {
		return err
	}
	return tx.Commit()
}

// A goroutine's slow server holds nothing of the caller's.
func (s *service) inAGoroutine(tx pgx.Tx) error {
	go func() {
		_, _ = s.provider.Resolve("main")
	}()
	go s.provider.Resolve("main")
	return tx.Commit()
}

// A goroutine that names the transaction is using it.
func (s *service) goroutineNamingIt(tx pgx.Tx) {
	go func() {
		_ = tx.Commit()
		_, _ = s.provider.Resolve("main") // want `contacts a Git provider`
	}()
}

// A goroutine's arguments are evaluated by its caller, with the transaction open.
func (s *service) goroutineArguments(tx pgx.Tx, sink func(string, error)) error {
	go sink(s.github.Mint()) // want `contacts a Git provider`
	return tx.Commit()
}

var shared *db.Queries

// A package's own queries are not a transaction's.
func (s *service) packageQueries() error {
	if err := shared.Read(); err != nil {
		return err
	}
	_, err := s.provider.Resolve("main")
	return err
}

func (b *box[T]) mint[U any]() (string, error) { return b.service.github.Mint() }

func (s *service) throughAGenericMethod(tx pgx.Tx) {
	b := &box[int]{s}
	_, _ = b.mint[string]() // want `contacts a Git provider`
}

type keyed struct{ broker *secretbroker.Broker }

// Keys are begun before the transaction and only used inside it.
func (k *keyed) keysFirst(run func(func(pgx.Tx) error) error) error {
	keys, err := k.broker.Begin()
	if err != nil {
		return err
	}
	return run(func(tx pgx.Tx) error {
		_, err := keys.Open(nil)
		return err
	})
}

func (k *keyed) keysUnderTransaction(tx pgx.Tx) error {
	_, err := k.broker.Begin() // want `the secret broker`
	return err
}

// The store admits a caller in a transaction of its own before asking for keys.
func keysThroughStore(st *store.Store, tx pgx.Tx) error {
	_ = st.Audit()
	return st.BeginKeys() // want `the secret broker`
}
