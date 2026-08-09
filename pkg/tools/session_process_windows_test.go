//go:build windows

package tools

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKillProcessGroupWithPropagatesTerminationFailure(t *testing.T) {
	want := errors.New("taskkill failed")
	err := killProcessGroupWith(42, func(int) error { return want })
	require.ErrorIs(t, err, want)
}
