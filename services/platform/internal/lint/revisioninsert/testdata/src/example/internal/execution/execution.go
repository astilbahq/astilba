package execution

import (
	db "example/db/generated"
	"example/internal/store"
)

type other struct{}

func (other) InsertRevision(arg db.InsertRevisionParams) error { return nil }

func write(q *db.Queries) error {
	if err := store.InsertRevision(q, db.InsertRevisionParams{}); err != nil {
		return err
	}
	if err := q.GetRevision(); err != nil {
		return err
	}
	if err := (other{}).InsertRevision(db.InsertRevisionParams{}); err != nil {
		return err
	}
	insert := q.InsertRevision // want `use store.InsertRevision`
	_ = insert
	return q.InsertRevision(db.InsertRevisionParams{}) // want `use store.InsertRevision`
}
