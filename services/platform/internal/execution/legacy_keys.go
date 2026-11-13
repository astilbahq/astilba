package execution

import (
	"encoding/json/v2"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// The paths below still open and seal integrations with the installation keyring directly,
// without a record: the agent delivery path, Git access during multi-pass work, and GitHub
// grants. They move to the secret broker with "need keys" passes and release records
// (PLAT-243, PLAT-196), and this file then goes. The secretkeys analyzer allows nothing new.

func (s *Service) legacySealIntegration(ws, id uuid.UUID, kind string, value any) ([]byte, error) {
	raw, err := stablejson.Marshal(value)
	if err != nil {
		return nil, err
	}
	return s.Store.Keys.Seal(raw, pipeline.EncryptionIdentity(ws, id, kind))
}

func (s *Service) legacyOpenIntegration(ws, id uuid.UUID, kind string, raw []byte, value any) error {
	plain, err := s.Store.Keys.Open(raw, pipeline.EncryptionIdentity(ws, id, kind))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, value)
}
