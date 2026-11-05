package db

type Account struct{ AuthVersion int64 }

type Queries struct{}

func (*Queries) AccountByID() (Account, error) { return Account{}, nil }
func (*Queries) DeleteAccount() error          { return nil }
