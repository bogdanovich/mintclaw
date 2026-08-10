//go:build !windows

package nodes

import "os"

func openGatewayInvocationFile(path string) (*os.File, error) {
	return os.Open(path)
}
