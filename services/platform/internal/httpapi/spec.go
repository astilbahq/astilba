package httpapi

import (
	"sync"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// Nothing changes the specification once it is loaded, and every request already reads it.
var loadSpec = sync.OnceValues(api.GetSpec)
