//go:build linux

package coordinator

import "context"

func (store *Store) validatePlatformSignature(
	_ context.Context,
	_ string,
	_ bool,
	_ Installation,
) error {
	return nil
}
