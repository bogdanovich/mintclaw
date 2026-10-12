//go:build !linux

package browserworker

import (
	"context"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
)

func StartSupervised(context.Context, approval.LoginLease, CurrentScope, AdmitSubmission) (*Session, error) {
	return nil, errUnavailable
}
