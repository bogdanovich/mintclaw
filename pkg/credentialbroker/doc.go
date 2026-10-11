// Package credentialbroker implements the private credential service's
// authorization foundation. It contains no secret retrieval or model tools.
//
// Approve, Revoke, and ReplacePolicy are trusted control-plane entry points.
// A transport must authenticate the operator and verify a scope-bound approval
// before calling Approve; passing model-provided identity or approval is unsafe.
// Authorize is a point-in-time check, not a reservation: consumers must recheck
// immediately before private injection and on every authenticated operation.
// Browser-session cancellation and isolation are separate integration duties.
package credentialbroker
