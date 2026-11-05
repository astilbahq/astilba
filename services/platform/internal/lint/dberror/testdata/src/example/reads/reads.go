package reads

import (
	"errors"

	db "example/db/generated"

	"github.com/jackc/pgx/v5"
)

var Forbidden = errors.New("forbidden")

// The shapes reviews caught: #122 returned a refusal, #147 ended a valid session and
// browserPrincipal answered a failed read as signed out.
func refusal(q *db.Queries) error {
	_, err := q.AccountByID()
	if err != nil { // want `a failed AccountByID would read as a refusal or a missing row`
		return Forbidden
	}
	return nil
}

func compound(q *db.Queries, version int64) bool {
	account, err := q.AccountByID()
	if err != nil || account.AuthVersion != version { // want `a failed AccountByID`
		return false
	}
	return true
}

// #147: the branch deleted a valid session, and its only use of e was a new assignment.
func replaced(q *db.Queries, version int64, drop func() error) error {
	account, e := q.AccountByID()
	if e != nil || account.AuthVersion != version { // want `a failed AccountByID`
		if e = drop(); e != nil {
			return e
		}
	}
	return nil
}

// A handler's bare return says nothing about the error: it has no result to carry it.
func handler(q *db.Queries, fail func()) {
	if _, err := q.AccountByID(); err != nil { // want `a failed AccountByID`
		fail()
		return
	}
}

func initialised(tx pgx.Tx) error {
	var v string
	if err := tx.QueryRow("x").Scan(&v); err != nil { // want `a failed Scan`
		return Forbidden
	}
	return nil
}

func separated(q *db.Queries) error {
	_, err := q.AccountByID()
	other := 1
	if other == 2 || err != nil { // want `a failed AccountByID`
		return Forbidden
	}
	return nil
}

func elseIf(q *db.Queries, skip bool) error {
	if skip {
		return nil
	} else if _, err := q.AccountByID(); err != nil { // want `a failed AccountByID`
		return Forbidden
	}
	return nil
}

func inACase(q *db.Queries, kind int) error {
	switch kind {
	case 1:
		if _, err := q.AccountByID(); err != nil { // want `a failed AccountByID`
			return Forbidden
		}
	}
	return nil
}

// err is nil past a branch that returns, so this later read says nothing about the failure.
func laterRead(q *db.Queries, sink func(error)) error {
	_, err := q.AccountByID()
	if err != nil { // want `a failed AccountByID`
		return Forbidden
	}
	sink(err)
	return nil
}

func namedOverwritten(q *db.Queries) (err error) {
	if _, err = q.AccountByID(); err != nil { // want `a failed AccountByID`
		err = Forbidden
		return
	}
	return nil
}

// A sqlc :exec method cannot report a missing row.
func writes(q *db.Queries, count func()) {
	if err := q.DeleteAccount(); err != nil {
		count()
	}
}

func classified(q *db.Queries) error {
	_, err := q.AccountByID()
	if errors.Is(err, pgx.ErrNoRows) {
		return Forbidden
	}
	if err != nil {
		return err
	}
	return nil
}

func classifiedInside(q *db.Queries) error {
	if _, err := q.AccountByID(); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Forbidden
		}
		return err
	}
	return nil
}

func classifiedInCondition(q *db.Queries) error {
	_, err := q.AccountByID()
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}

// A login compares a dummy hash for a missing account, then branches on what is left.
func classifiedEarlier(q *db.Queries) string {
	_, err := q.AccountByID()
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "failed"
	}
	if err != nil {
		return "missing"
	}
	return "found"
}

func classifiedInElseIf(q *db.Queries) string {
	_, err := q.AccountByID()
	if errors.Is(err, pgx.ErrNoRows) {
		return "missing"
	} else if err != nil {
		return "failed"
	}
	return "found"
}

func namedReturnedLater(q *db.Queries, cleanup func()) (err error) {
	_, err = q.AccountByID()
	if err != nil {
		cleanup()
	}
	return
}

func retried(q *db.Queries, wrap func(error) error) error {
	var last error
	for range 3 {
		_, last = q.AccountByID()
		if last != nil {
			continue
		}
		return nil
	}
	return wrap(last)
}

func named(q *db.Queries) (err error) {
	if _, err = q.AccountByID(); err != nil {
		return
	}
	return nil
}

func returnedLater(q *db.Queries, cleanup func()) error {
	_, err := q.AccountByID()
	if err != nil {
		cleanup()
	}
	return err
}

func rewrapped(q *db.Queries, wrap func(error) error) error {
	_, err := q.AccountByID()
	if err != nil {
		err = wrap(err)
		return err
	}
	return nil
}

func notADatabaseCall(parse func() error) error {
	if err := parse(); err != nil {
		return Forbidden
	}
	return nil
}

// Exec has no row to miss, so this cannot mistake one failure for another.
func exec(tx pgx.Tx, drop func()) {
	if _, err := tx.Exec("x"); err != nil {
		drop()
	}
}
