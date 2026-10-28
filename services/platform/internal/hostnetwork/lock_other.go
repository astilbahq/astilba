//go:build !linux && !darwin

package hostnetwork

import "errors"

func (h Host) lock() (func(), error) {
	return nil, errors.New("managed network host locking is unsupported")
}
