//go:build !windows

package companion

import "os"

func currentCodingProcessPrivileged() (bool, error) {
	return os.Geteuid() == 0, nil
}
