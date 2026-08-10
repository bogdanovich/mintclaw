//go:build !linux

package companion

// These shared contracts are implemented only by Linux backends. Keeping
// explicit compile-time references on unsupported platforms lets portable
// static analysis distinguish them from genuinely dead declarations without
// duplicating the protocol shapes across build-tagged files.
var (
	_ preparedAuthorityBrokerTerminal
	_ = AuthorityBrokerConfig.prepareTerminal
	_ authorityBrokerResponseFrame
	_ = TerminalBrokerOpenRequest.validate
	_ = fileHelperSnapshot.descriptors
	_ serviceActionAcceptor
	_ serviceActionExecutor
	_ = unavailableServiceStatus
)
