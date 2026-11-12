package agent

import "example.com/platform/internal/cryptobox"

// Only the module's own internal/agent is exempt, not a package whose path ends the same way.
func load() { _, _ = cryptobox.Load("path") } // want `cryptobox.Load gives keys`
