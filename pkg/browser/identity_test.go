package browser

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestOpaqueRuntimeIdentityPreservesPersistedProjection(t *testing.T) {
	execution := "runtime-execution"
	call := "tool-call"
	executionHash := sha256.Sum256([]byte("execution\x00" + execution))
	requestHash := sha256.Sum256([]byte("request\x00" + execution + "\x00" + call))
	if OpaqueExecutionID(" "+execution+" ") != "execution_"+hex.EncodeToString(executionHash[:16]) ||
		OpaqueRequestID(" "+execution+" ", " "+call+" ") != "request_"+hex.EncodeToString(requestHash[:16]) ||
		OpaqueExecutionID(execution) == OpaqueExecutionID("trace-turn") ||
		OpaqueRequestID(execution, call) == OpaqueRequestID(execution, "another-call") {
		t.Fatal("runtime identity projection changed or collapsed distinct identities")
	}
}
