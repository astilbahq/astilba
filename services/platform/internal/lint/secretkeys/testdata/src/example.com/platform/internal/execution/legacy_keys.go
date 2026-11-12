package execution

// A file still waiting to move to the broker is reported nowhere.
func (s *Service) legacy(v []byte) ([]byte, error) { return s.keys.Seal(v, "identity") }
