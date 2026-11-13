package execution

// A caller that moves with the legacy file may still call it.
func (s *Service) hydrate(v []byte) ([]byte, error) { return s.legacy(v) } // want hydrate:"transitional"

// A listed caller's own entry point reaches keys too, and is held to the list in other packages.
func (s *Service) Hydrate(v []byte) ([]byte, error) { return s.hydrate(v) } // want Hydrate:"transitional"
