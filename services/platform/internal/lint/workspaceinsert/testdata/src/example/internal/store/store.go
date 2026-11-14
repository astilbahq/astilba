package store

import db "example/db/generated"

type Store struct{}

func CreateWorkspace(q *db.Queries, name string) error {
	if err := q.InsertWorkspace(db.InsertWorkspaceParams{Name: name}); err != nil {
		return err
	}
	return q.InsertMembership()
}

// A method of the same name is not the constructor.
func (Store) CreateWorkspace(q *db.Queries) error {
	return q.InsertWorkspace(db.InsertWorkspaceParams{}) // want `use store.CreateWorkspace`
}

func bootstrap(q *db.Queries) error {
	return q.InsertWorkspace(db.InsertWorkspaceParams{}) // want `use store.CreateWorkspace`
}
