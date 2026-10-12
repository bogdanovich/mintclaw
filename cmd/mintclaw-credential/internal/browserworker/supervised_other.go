//go:build !linux

package browserworker

import (
	"context"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/approval"
)

func StartSupervised(context.Context, approval.LoginLease, CurrentScope, AdmitSubmission) (*Session, error) {
	return nil, errUnavailable
}
