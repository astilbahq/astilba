package httpapi

import (
	"example/internal/api"
	"example/internal/store"
)

func Elsewhere(p store.Principal) api.Operation { // want `an operation reaches a principal only through package execution`
	return api.Operation{}
}

func Unrelated() api.Operation { return api.Operation{} }
