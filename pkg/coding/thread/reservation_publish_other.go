//go:build !linux && !darwin && !windows

package thread

import (
	"errors"
	"os"
)

func renameThreadReservationNoReplace(*os.Root, string, string) error {
	return errors.New("coding thread store: atomic reservation publication is unsupported on this platform")
}
