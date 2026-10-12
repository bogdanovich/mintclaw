package browserworker

import (
	"context"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/approval"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestSupervisedStartupRequiresActualAuthority(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background()} {
		session, err := StartSupervised(ctx, approval.LoginLease{},
			func(context.Context) (credentialbroker.Request, error) {
				t.Fatal("denied startup evaluated caller scope")
				return credentialbroker.Request{}, nil
			}, func(context.Context, credentialbroker.LoginFlow) error {
				t.Fatal("denied startup evaluated effect authority")
				return nil
			})
		if session != nil || !errors.Is(err, errUnavailable) {
			t.Fatal("startup without actual approval was admitted")
		}
	}
}
