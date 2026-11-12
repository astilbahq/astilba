package store

type Store struct{}

func (*Store) BeginKeys() error { return nil }
func (*Store) Audit() error     { return nil }
