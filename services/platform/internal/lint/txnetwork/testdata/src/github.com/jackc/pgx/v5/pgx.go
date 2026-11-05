package pgx

type Tx interface{ Commit() error }
