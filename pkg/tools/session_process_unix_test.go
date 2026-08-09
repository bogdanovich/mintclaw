//go:build !windows

package tools

import (
	"errors"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKillProcessGroupWithPropagatesTerminationFailure(t *testing.T) {
	groupErr := errors.New("group kill failed")
	processErr := errors.New("process kill failed")
	err := killProcessGroupWith(42, func(pid int, _ syscall.Signal) error {
		if pid < 0 {
			return groupErr
		}
		return processErr
	})
	require.ErrorIs(t, err, groupErr)
	require.ErrorIs(t, err, processErr)
}
