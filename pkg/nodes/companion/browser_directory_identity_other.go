//go:build !darwin && !linux

package companion

import (
	"errors"
	"os"
)

func validateBrowserProfileDirectory(os.FileInfo) error {
	return errors.New("profile_directory ownership validation is unsupported on this platform")
}

func validateBrowserDriverDirectory(os.FileInfo) error {
	return errors.New("driver directory ownership validation is unsupported on this platform")
}

func validateBrowserExecutableFile(os.FileInfo) error {
	return errors.New("browser executable identity validation is unsupported on this platform")
}
