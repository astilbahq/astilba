package execution

import (
	"errors"

	"example/internal/api"
	"example/internal/store"
)

func operationFor(p store.Principal, op api.Operation) api.Operation { return op }

func operationsFor(p store.Principal, ops []api.Operation) []api.Operation { return ops }

func scan() (api.Operation, error) { return api.Operation{}, nil }

func Shown(p store.Principal) (api.Operation, error) {
	out, err := scan()
	if err != nil {
		return operationFor(p, out), err
	}
	return operationFor(p, out), nil
}

func Listed(p store.Principal) ([]api.Operation, error) {
	return operationsFor(p, []api.Operation{}), nil
}

func Raw(p store.Principal) (api.Operation, error) {
	out, err := scan()
	if err != nil {
		return api.Operation{}, err
	}
	if out.Note == "" {
		return api.Operation{Note: "made"}, nil // want `return the operation through operationFor`
	}
	return out, nil // want `return the operation through operationFor`
}

func Named(p *store.Principal) (out api.Operation, err error) {
	return // want `return the operation through operationFor`
}

func RawList(p store.Principal) ([]api.Operation, error) {
	if true {
		return nil, errors.New("no")
	}
	ops, _ := RawList(p)
	return ops, nil // want `return the operation through operationsFor`
}

// A literal's own returns are its own: only the function's returns reach the principal.
func Closure(p store.Principal) (api.Operation, error) {
	var out api.Operation
	err := func() error {
		var err error
		out, err = scan()
		return err
	}()
	return operationFor(p, out), err
}

// Without a principal, an operation goes to management's own work, not to a caller.
func internal() (api.Operation, error) { return scan() }

// An unexported function works for an exported one, which shows the principal what it returns.
func admit(p store.Principal) (api.Operation, error) { return scan() }

type Service struct{}

func (s *Service) Method(p store.Principal) (api.Operation, error) {
	return scan() // want `return the operation through operationFor`
}
