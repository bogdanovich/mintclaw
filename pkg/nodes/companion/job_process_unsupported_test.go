//go:build !linux && !darwin

package companion

import (
	"errors"
	"testing"
)

func TestDirectJobManagerRejectsUnsupportedPlatform(t *testing.T) {
	_, err := NewDirectJobManager(
		&JobStore{},
		SystemExecPolicy{},
		"test-jobs",
		"job-profile-v1",
		DirectJobLimits{},
	)
	if !errors.Is(err, ErrJobPlatformUnsupported) {
		t.Fatalf("NewDirectJobManager() error = %v", err)
	}
}
