package httpapi

import "example.com/platform/internal/execution"

func handle(s *execution.Service) {
	_, _ = s.Exported(nil) // want `Exported, which opens values without a record, gives keys`
}
