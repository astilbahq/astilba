package deployment

import "github.com/google/uuid"

// Replacement records that a plan moves the environment off its revoked primary, and the volumes
// the owner accepted starting empty, because their data stays on that server.
type Replacement struct {
	Server           uuid.UUID   `json:"server"`
	AbandonedVolumes []uuid.UUID `json:"abandonedVolumes"`
}
