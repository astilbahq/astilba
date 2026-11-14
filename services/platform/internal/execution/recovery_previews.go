package execution

import (
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// dropReleasedPreviewSecrets leaves out the secrets of every preview environment no open request
// names, which recovery would otherwise take from a checkpoint captured before the preview was
// released. Releasing a preview, by its cleanup or by revoking its preview host, deletes that
// environment's copies of its template's secret values in the same step as it marks the request
// deleted, and a request that comes back gets a new environment, so nothing needs them again. The
// requests are management's where it holds them, as for every row both sides hold, so a cleanup the
// restored database records stands over a checkpoint taken before it.
func dropReleasedPreviewSecrets(a *portability.Archive) error {
	released := map[uuid.UUID]bool{}
	for _, row := range a.Tables["environments"] {
		purpose, err := recoveryValue[string](row, "purpose")
		if err != nil {
			return err
		}
		if purpose == "preview" {
			id, err := recoveryValue[uuid.UUID](row, "id")
			if err != nil {
				return err
			}
			released[id] = true
		}
	}
	for _, row := range a.Tables["preview_requests"] {
		env, err := recoveryValue[*uuid.UUID](row, "environment_id")
		if err != nil {
			return err
		}
		state, err := recoveryValue[string](row, "state")
		if err != nil {
			return err
		}
		if env != nil && state != "deleted" {
			delete(released, *env)
		}
	}
	dropped := map[uuid.UUID]bool{}
	kept := []portability.Row{}
	for _, row := range a.Tables["secrets"] {
		env, err := recoveryValue[uuid.UUID](row, "environment_id")
		if err != nil {
			return err
		}
		if released[env] {
			id, err := recoveryValue[uuid.UUID](row, "id")
			if err != nil {
				return err
			}
			dropped[id] = true
			continue
		}
		kept = append(kept, row)
	}
	a.Tables["secrets"] = kept
	kept = []portability.Row{}
	for _, row := range a.Tables["secret_versions"] {
		secret, err := recoveryValue[uuid.UUID](row, "secret_id")
		if err != nil {
			return err
		}
		if !dropped[secret] {
			kept = append(kept, row)
		}
	}
	a.Tables["secret_versions"] = kept
	return nil
}
