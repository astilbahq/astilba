package pipeline

import "github.com/google/uuid"

func ValidCacheID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Commits deliberately share a cache; BuildKit still hashes their source contents.
// Secret versions are inputs because BuildKit does not hash secret mount values.
func (c Command) CacheKey() string {
	b := c.Build
	return Hash(struct {
		Builder, Branch, Context, Dockerfile, Architecture string
		Connection                                         Connection
		Arguments                                          map[string]string
		Secrets                                            map[string]uuid.UUID
	}{BuilderImage, b.Branch, b.Context, b.Dockerfile, b.Architecture, c.Connection, b.Arguments, b.Secrets})
}
