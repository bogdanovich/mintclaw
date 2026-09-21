package companion

import "golang.org/x/sys/windows"

func currentCodingProcessPrivileged() (bool, error) {
	return windows.GetCurrentProcessToken().IsElevated(), nil
}
