package intent

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const reference = "0123456789abcdef0123456789abcdef"

func TestIntentRoundTripCarriesOnlyAdmittedScope(t *testing.T) {
	for _, request := range []Request{
		{Operation: Status},
		{Operation: RequestApproval, Accounts: []string{"facebook", "ebay"}, DurationSeconds: 72000},
		{Operation: ApprovalStatus, ApprovalID: reference},
		{Operation: GrantStatus, GrantID: reference},
		{Operation: Login, Account: "facebook", GrantID: reference},
	} {
		data, err := EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeRequest(data)
		if err != nil || !reflect.DeepEqual(request, decoded) {
			t.Fatal("admitted request lost its scope")
		}
	}
}

func TestMalformedIntentsAndIdentitySubstitutionNeverEchoInput(t *testing.T) {
	const canary = "SYNTHETIC_PRIVATE_INPUT_CANARY"
	for _, data := range []string{
		`{"version":1,"operation":"status","owner_id":"` + canary + `"}`,
		`{"version":1,"operation":"status","password":"` + canary + `"}`,
		`{"version":1,"operation":"get_secret"}`,
		`{"version":1,"operation":"status","operation":"login"}`,
		`{"version":1,"operation":"status","Operation":"login"}`,
		`{"version":1,"operation":"status","Version":1}`,
		`{"version":1,"operation":"status"} {"password":"` + canary + `"}`,
		`{"version":2,"operation":"status"}`,
		`{"version":1,"operation":null}`,
		`{"version":1,"operation":"status","accounts":null}`,
		`{"version":1,"operation":"status","account":"` + canary + `"}`,
		`{"version":1,"operation":"request_approval","accounts":["facebook","facebook"],"duration_seconds":1}`,
		`{"version":1,"operation":"request_approval","accounts":["facebook"],"duration_seconds":72001}`,
		`{"version":1,"operation":"request_approval","accounts":["facebook"],"duration_seconds":0}`,
		`{"version":1,"operation":"request_approval","accounts":[{"password":"` + canary + `"}],"duration_seconds":1}`,
		`{"version":1,"operation":"login","account":"https://evil.example","grant_id":"` + reference + `"}`,
		`{"version":1,"operation":"login","account":"facebook","grant_id":"` + canary + `"}`,
		`["` + canary + `"]`,
		strings.Repeat(" ", MaxPacketBytes+1),
	} {
		request, err := DecodeRequest([]byte(data))
		if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), canary) || request.Operation != "" {
			t.Fatal("malformed intent was accepted, retained, or echoed")
		}
	}
}

func TestResponsesCannotCarryDiagnosticsOrCrossOperationAuthority(t *testing.T) {
	const canary = "SYNTHETIC_RESPONSE_SECRET_CANARY"
	for _, scenario := range []struct {
		operation Operation
		response  Response
	}{
		{Status, Response{Code: Code(canary)}},
		{Status, Response{Code: Ready, SessionID: canary}},
		{Status, Response{Code: LoggedIn, SessionID: reference}},
		{RequestApproval, Response{Code: ApprovalRequired, ApprovalID: reference, ValidForSeconds: 301}},
		{ApprovalStatus, Response{Code: GrantActive, GrantID: canary, ValidForSeconds: 1}},
		{GrantStatus, Response{Code: GrantActive, GrantID: reference, ValidForSeconds: 1}},
		{Login, Response{Code: LoggedIn, SessionID: canary}},
		{Login, Response{Code: Failed, SessionID: reference}},
	} {
		data, err := EncodeResponse(scenario.operation, scenario.response)
		if !errors.Is(err, ErrInvalid) || data != nil || strings.Contains(err.Error(), canary) {
			t.Fatal("unsafe response escaped the output schema")
		}
	}
	for _, data := range []string{
		`{"version":1,"code":"failed","error":"` + canary + `"}`,
		`{"version":1,"code":"failed","code":"ready"}`,
		`{"version":1,"code":"failed","Code":"ready"}`,
		`{"version":1,"code":"failed","grant_id":null}`,
	} {
		response, err := DecodeResponse(Status, []byte(data))
		if !errors.Is(err, ErrInvalid) || response.Code != "" || strings.Contains(err.Error(), canary) {
			t.Fatal("unsafe server response was accepted or echoed")
		}
	}
}

func TestScopedResponseRoundTrips(t *testing.T) {
	for _, scenario := range []struct {
		operation Operation
		response  Response
	}{
		{Status, Response{Code: Ready}},
		{Status, Response{Code: NotReady}},
		{RequestApproval, Response{Code: ApprovalRequired, ApprovalID: reference, ValidForSeconds: 300}},
		{ApprovalStatus, Response{Code: ApprovalPending, ValidForSeconds: 1}},
		{ApprovalStatus, Response{Code: ApprovalExpired}},
		{ApprovalStatus, Response{Code: GrantActive, GrantID: reference, ValidForSeconds: 72000}},
		{GrantStatus, Response{Code: GrantActive, ValidForSeconds: 1}},
		{GrantStatus, Response{Code: GrantInactive}},
		{Login, Response{Code: LoggedIn, SessionID: reference}},
		{Login, Response{Code: HandoffRequired, SessionID: reference}},
		{Login, Response{Code: OutcomeUnknown}},
	} {
		data, err := EncodeResponse(scenario.operation, scenario.response)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeResponse(scenario.operation, data)
		if err != nil || decoded != scenario.response || bytes.Contains(data, []byte("password")) {
			t.Fatal("scoped response lost its authority or admitted a password field")
		}
	}
}
