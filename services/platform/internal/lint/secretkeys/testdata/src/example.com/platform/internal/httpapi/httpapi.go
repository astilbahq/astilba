package httpapi

import "example.com/platform/internal/execution"

func handle(s *execution.Service) {
	_, _ = s.Exported(nil)        // want `Exported, which opens values without a record, gives keys`
	_, _ = s.Hydrate(nil)         // want `Hydrate, which opens values without a record, gives keys`
	_, _ = execution.Held(s, nil) // want `Held, which opens values without a record, gives keys`
}
