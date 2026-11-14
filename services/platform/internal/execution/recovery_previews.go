package execution

import (
	"maps"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// dropReleasedPreviewSecrets leaves out the secrets of every preview environment no open request
// names, which recovery would otherwise take from a checkpoint captured before the preview was
// released. Releasing a preview, by its cleanup or by revoking its preview host, deletes that
// environment's copies of its template's secret values in the same step as it marks the request
// deleted, and a request that comes back gets a new environment, so nothing needs them again. The
// merge keeps a request's deletion whichever side recorded it, so a cleanup either side records
// stands over the other's copy from before it.
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

// recoveryPreviewRequestKnowledge keeps the copy of a preview request that is further along. A
// request's generation advances only when it is allocated an environment. It holds that environment
// until it is deleted, and gives it up only when it comes back after that, so within a generation a
// deleted copy is newer than an open one, and a copy without the environment newer than both.
// Keeping the database's older copy would bring back a preview whose resources are gone, holding its
// slot and its environment's secret copies.
func recoveryPreviewRequestKnowledge(prior, incoming portability.Row) (portability.Row, error) {
	older, err := recoveryPreviewProgress(prior)
	if err != nil {
		return nil, err
	}
	newer, err := recoveryPreviewProgress(incoming)
	if err != nil {
		return nil, err
	}
	if newer[0] > older[0] || newer[0] == older[0] && newer[1] > older[1] {
		return maps.Clone(incoming), nil
	}
	return prior, nil
}

// recoveryPreviewProgress is how far a request has come: its generation, then whether it holds the
// generation's environment open (0), holds it deleted (1) or has given it up (2).
func recoveryPreviewProgress(row portability.Row) ([2]int64, error) {
	generation, err := recoveryValue[int64](row, "generation")
	if err != nil {
		return [2]int64{}, err
	}
	env, err := recoveryValue[*uuid.UUID](row, "environment_id")
	if err != nil {
		return [2]int64{}, err
	}
	state, err := recoveryValue[string](row, "state")
	if err != nil {
		return [2]int64{}, err
	}
	switch {
	case env == nil:
		return [2]int64{generation, 2}, nil
	case state == "deleted":
		return [2]int64{generation, 1}, nil
	}
	return [2]int64{generation, 0}, nil
}

// settleRecoveredPreviewPolicies keeps, of a preview policy's two copies, the one naming the host its
// open previews run on. Allocation places a preview on its policy's host, and a policy can't change
// host while it has one, but the database's copy can be from before the owner moved the policy to
// another host and previews opened there. Keeping it would send those previews' cleanup to a host
// they don't run on, which can never confirm it, so they and the policy would be stuck. Where the
// open previews don't decide it, the copy with the higher generation, which advances on every save,
// wins, and on a tie the database's copy stands.
func settleRecoveredPreviewPolicies(a portability.Archive, known, incoming recoveryRows) error {
	placed := map[uuid.UUID][]uuid.UUID{}
	for _, row := range a.Tables["deployment_units"] {
		env, err := recoveryValue[uuid.UUID](row, "environment_id")
		if err != nil {
			return err
		}
		server, err := recoveryValue[*uuid.UUID](row, "server_id")
		if err != nil {
			return err
		}
		if server != nil {
			placed[env] = append(placed[env], *server)
		}
	}
	runs := map[uuid.UUID]map[uuid.UUID]bool{}
	for _, row := range a.Tables["preview_requests"] {
		policy, err := recoveryValue[uuid.UUID](row, "policy_id")
		if err != nil {
			return err
		}
		env, err := recoveryValue[*uuid.UUID](row, "environment_id")
		if err != nil {
			return err
		}
		state, err := recoveryValue[string](row, "state")
		if err != nil {
			return err
		}
		if env == nil || state == "deleted" {
			continue
		}
		if runs[policy] == nil {
			runs[policy] = map[uuid.UUID]bool{}
		}
		for _, server := range placed[*env] {
			runs[policy][server] = true
		}
	}
	for i, row := range a.Tables["preview_policies"] {
		key, err := portability.RecoveryRowKey("preview_policies", row)
		if err != nil {
			return err
		}
		prior, kept := known["preview_policies"][key]
		checkpoint, carried := incoming["preview_policies"][key]
		if !kept || !carried {
			continue
		}
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return err
		}
		newer, err := recoveryPreviewPolicyChoice(prior, checkpoint, runs[id])
		if err != nil {
			return err
		}
		if newer {
			a.Tables["preview_policies"][i] = maps.Clone(checkpoint)
		}
	}
	return nil
}

// recoveryPreviewPolicyChoice reports whether the checkpoint's copy of a policy wins over the
// database's, given the hosts the policy's open previews run on.
func recoveryPreviewPolicyChoice(prior, checkpoint portability.Row, hosts map[uuid.UUID]bool) (bool, error) {
	if len(hosts) == 1 {
		x, err := recoveryValue[uuid.UUID](prior, "server_id")
		if err != nil {
			return false, err
		}
		y, err := recoveryValue[uuid.UUID](checkpoint, "server_id")
		if err != nil {
			return false, err
		}
		if hosts[x] != hosts[y] {
			return hosts[y], nil
		}
	}
	x, err := recoveryValue[int64](prior, "generation")
	if err != nil {
		return false, err
	}
	y, err := recoveryValue[int64](checkpoint, "generation")
	if err != nil {
		return false, err
	}
	return y > x, nil
}
