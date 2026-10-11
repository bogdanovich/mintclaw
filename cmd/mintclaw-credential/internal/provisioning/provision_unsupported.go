//go:build !linux && !darwin

package provisioning

import "context"

func Provision(context.Context) error {
	return ErrUnsupported
}
