//go:build !linux

package agent

import "errors"

// builderSeccompProfile has nothing to return off Linux, where builds don't run.
func builderSeccompProfile() (string, error) {
	return "", errors.New("builds need Linux")
}
