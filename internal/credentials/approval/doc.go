// Package approval owns runtime-internal passkey ceremonies, immutable policy,
// and revocable grants. The trusted runtime owns its lifecycle and private owner
// setup capability. It supplies phone handlers but no listener, notification
// transport, token reader, provider operation, or model tool. Legacy appliance
// console helpers remain unused by the integrated runtime.
package approval
