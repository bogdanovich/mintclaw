// Package supervision privately owns the kernel process forest of a credential
// browser worker. Only protected Root startup may construct it. It does not
// establish browser role, filesystem/network isolation, or provider readiness,
// and no executable command or agent endpoint currently activates it.
package supervision
