//go:build integration

package browserhost

import (
	browserworker "github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

// NewBrowserHostForIntegration builds the real companion host around controlled
// worker factories. Integration tests use it to exercise the gateway transport
// and host authority boundary without starting an external browser driver.
func NewBrowserHostForIntegration(
	profiles map[string]companion.BrowserProfilePolicy,
	factories map[string]browserworker.WorkerFactory,
) (*BrowserHost, error) {
	hostFactories := make(map[string]browserHostFactory, len(factories))
	for alias, factory := range factories {
		hostFactories[alias] = factory
	}
	host, err := newBrowserHost(profiles, hostFactories)
	if err != nil {
		return nil, err
	}
	host.verifyProfile = func(companion.BrowserProfilePolicy) error { return nil }
	return host, nil
}
