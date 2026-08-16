package nodes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	nodepkg "github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestAndroidEnrollmentCommandRequestsOfferAndPrintsJSON(t *testing.T) {
	const token = "operator-token"
	manager := nodepkg.NewEnrollmentOfferManager(nodepkg.EnrollmentOfferConfig{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != enrollmentOperatorPath ||
			request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(writer, "unexpected request", http.StatusBadRequest)
			return
		}
		var input nodepkg.EnrollmentOfferRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if input.Endpoint != "wss://gateway.example/nodes/v1/ws" || input.TTLSeconds != 60 {
			http.Error(writer, "unexpected offer input", http.StatusBadRequest)
			return
		}
		offer, err := manager.Issue(input.Endpoint, input.SPKISHA256, time.Duration(input.TTLSeconds)*time.Second)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		uri, err := nodepkg.EncodeEnrollmentOfferURI(offer)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(nodepkg.EnrollmentOfferResponse{Offer: offer, URI: uri})
	}))
	defer server.Close()
	cfg := terminalSmokeTestConfig(t, server.URL, token)
	cmd := newAndroidEnrollmentCommand(func() (*config.Config, error) { return cfg, nil })
	cmd.SetArgs([]string{
		"--endpoint", "wss://gateway.example/nodes/v1/ws", "--ttl", "60s", "--json",
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result nodepkg.EnrollmentOfferResponse
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if _, err := nodepkg.DecodeEnrollmentOfferURI(result.URI); err != nil {
		t.Fatalf("invalid URI in output: %v", err)
	}
}

func TestAndroidEnrollmentCommandPrintsQRAndSeparateApprovalReminder(t *testing.T) {
	const token = "operator-token"
	manager := nodepkg.NewEnrollmentOfferManager(nodepkg.EnrollmentOfferConfig{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input nodepkg.EnrollmentOfferRequest
		_ = json.NewDecoder(request.Body).Decode(&input)
		offer, err := manager.Issue(input.Endpoint, input.SPKISHA256, time.Duration(input.TTLSeconds)*time.Second)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		uri, _ := nodepkg.EncodeEnrollmentOfferURI(offer)
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(nodepkg.EnrollmentOfferResponse{Offer: offer, URI: uri})
	}))
	defer server.Close()
	cfg := terminalSmokeTestConfig(t, server.URL, token)
	cmd := newAndroidEnrollmentCommand(func() (*config.Config, error) { return cfg, nil })
	cmd.SetArgs([]string{"--endpoint", "wss://gateway.example/nodes/v1/ws"})
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"Enrollment URI: mintclaw://enroll/v1?data=", "approve the pending node separately"} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("output missing %q: %s", fragment, output.String())
		}
	}
}

func TestAndroidEnrollmentCommandValidatesTTLBeforeRequest(t *testing.T) {
	for _, ttl := range []string{"500ms", "6m"} {
		cmd := newAndroidEnrollmentCommand(func() (*config.Config, error) {
			t.Fatal("configuration loaded for invalid TTL")
			return nil, nil
		})
		cmd.SetArgs([]string{"--endpoint", "wss://gateway.example/nodes/v1/ws", "--ttl", ttl})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "between 1s and 5m") {
			t.Fatalf("ttl %s error = %v", ttl, err)
		}
	}
}
