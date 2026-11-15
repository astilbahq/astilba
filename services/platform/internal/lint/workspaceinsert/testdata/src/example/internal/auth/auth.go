package auth

import (
	db "example/db/generated"
	"example/internal/store"
)

type other struct{}

func (other) InsertWorkspace(arg db.InsertWorkspaceParams) error { return nil }

func signup(q *db.Queries) error {
	if err := store.CreateWorkspace(q, "Acme"); err != nil {
		return err
	}
	if err := (other{}).InsertWorkspace(db.InsertWorkspaceParams{}); err != nil {
		return err
	}
	insert := q.InsertWorkspace // want `use store.CreateWorkspace`
	_ = insert
	return q.InsertWorkspace(db.InsertWorkspaceParams{}) // want `use store.CreateWorkspace`
}
