// Package runtimeimage admits an operator-installed, read-only Linux browser
// runtime for later Root startup. It neither launches a worker nor initializes
// the vault. Role, namespace, network, IPC and process-retirement admission are
// separate prerequisites before the private browser may receive credentials.
package runtimeimage
