package pgx

import "errors"

var ErrNoRows = errors.New("no rows in result set")

type Row interface{ Scan(dest ...any) error }

type Tx interface {
	QueryRow(sql string) Row
	Exec(sql string) (int64, error)
}
