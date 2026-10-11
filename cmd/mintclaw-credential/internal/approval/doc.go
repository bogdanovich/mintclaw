// Package approval owns the standalone credential service's passkey ceremonies,
// immutable policy, and grants. It has no HTTP listener, notification transport,
// token reader, provider operation, or model tool. Trusted startup must qualify
// service/process isolation and protect both the owner store and initial setup
// capability before admitting a control handler.
package approval
