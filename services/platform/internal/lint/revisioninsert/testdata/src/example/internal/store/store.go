package store

import db "example/db/generated"

func InsertRevision(q *db.Queries, arg db.InsertRevisionParams) error {
	return q.InsertRevision(arg)
}
