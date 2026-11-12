package execution

// A caller that moves with the legacy file may still call it.
func (s *Service) hydrate(v []byte) ([]byte, error) { return s.legacy(v) }
