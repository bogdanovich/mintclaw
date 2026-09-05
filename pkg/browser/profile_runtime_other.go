//go:build !darwin && !linux

package browser

import (
	"errors"
	"os"
)

func validateBrowserRuntimeOwner(os.FileInfo, bool) error {
	return errors.New("browser runtime ownership validation is unsupported")
}
