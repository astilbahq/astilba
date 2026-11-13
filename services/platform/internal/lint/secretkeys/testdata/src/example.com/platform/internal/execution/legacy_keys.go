package execution

// A file still waiting to move to the broker is reported nowhere.
func (s *Service) legacy(v []byte) ([]byte, error) { return s.keys.Seal(v, "identity") } // want legacy:"transitional"

// A wrapper in a pending file reaches keys through the function it calls.
func (s *Service) wrapper(v []byte) ([]byte, error) { return s.legacy(v) } // want wrapper:"transitional"

// An exported one can be called from another package.
func (s *Service) Exported(v []byte) ([]byte, error) { return s.wrapper(v) } // want Exported:"transitional"

// A variable can hold a transitional function too.
var Held = (*Service).legacy // want Held:"transitional"

// A type whose methods reach keys is reached by making one, whatever interface calls it.
type opener interface{ open([]byte) ([]byte, error) }

type legacyOpener struct{ s *Service }

func (o legacyOpener) open(v []byte) ([]byte, error) { return o.s.keys.Open(v, "identity") } // want open:"transitional"
