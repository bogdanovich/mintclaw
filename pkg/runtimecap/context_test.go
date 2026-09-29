package runtimecap

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

type testArtifacts struct{}

func (*testArtifacts) Store(string, media.MediaMeta, string) (string, error) {
	return "media://test", nil
}
func (*testArtifacts) Resolve(string) (string, error) { return "/tmp/test", nil }
func (*testArtifacts) ResolveWithMeta(string) (string, media.MediaMeta, error) {
	return "/tmp/test", media.MediaMeta{}, nil
}
func (*testArtifacts) ReleaseAll(string) error { return nil }

type testDelivery struct{}

func (*testDelivery) SendMessage(context.Context, bus.OutboundMessage) error    { return nil }
func (*testDelivery) SendMedia(context.Context, bus.OutboundMediaMessage) error { return nil }

type testBrowser struct{ available bool }

func (browser *testBrowser) Available() bool { return browser.available }

func TestContextReportsTypedOptionalDependencies(t *testing.T) {
	var typedNilArtifacts *testArtifacts
	var typedNilDelivery *testDelivery
	var typedNilBrowser *testBrowser
	runtime := NewContext(Inputs{
		Kind: KindCoding, Artifacts: typedNilArtifacts, Delivery: typedNilDelivery, Browser: typedNilBrowser,
	})

	assertAvailability(t, runtime.Report(), CapabilityRuntimePrincipal, false, ReasonIdentityIncomplete)
	assertAvailability(t, runtime.Report(), CapabilityArtifactRead, false, ReasonNotConfigured)
	assertAvailability(t, runtime.Report(), CapabilityArtifactWrite, false, ReasonNotConfigured)
	assertAvailability(t, runtime.Report(), CapabilityChannelDelivery, false, ReasonNotConfigured)
	assertAvailability(t, runtime.Report(), CapabilityBrowserClient, false, ReasonNotConfigured)

	runtime = NewContext(Inputs{
		Kind: KindCoding, BrowserUnavailableReason: ReasonPolicyDisabled,
	})
	assertAvailability(t, runtime.Report(), CapabilityBrowserClient, false, ReasonPolicyDisabled)

	runtime = NewContext(Inputs{
		Kind: KindGateway, Artifacts: &testArtifacts{}, Delivery: &testDelivery{}, Browser: &testBrowser{},
	})
	assertAvailability(t, runtime.Report(), CapabilityArtifactRead, true, "")
	assertAvailability(t, runtime.Report(), CapabilityArtifactWrite, true, "")
	assertAvailability(t, runtime.Report(), CapabilityChannelDelivery, true, "")
	assertAvailability(t, runtime.Report(), CapabilityBrowserClient, false, ReasonServiceUnavailable)
}

func TestContextBindsValidatedPrincipalImmutably(t *testing.T) {
	base := NewContext(Inputs{Kind: KindCoding})
	principal := Principal{
		Runtime: KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: "thread-1", ExecutionID: "execution-1",
	}
	bound := base.BindPrincipal(principal)

	if _, ok := base.Principal(); ok {
		t.Fatal("BindPrincipal mutated the construction-time context")
	}
	if got, ok := bound.Principal(); !ok || got != principal {
		t.Fatalf("bound principal = (%+v, %t), want %+v", got, ok, principal)
	}
	assertAvailability(t, bound.Report(), CapabilityRuntimePrincipal, true, "")

	wrongRuntime := principal
	wrongRuntime.Runtime = KindGateway
	if _, ok := base.BindPrincipal(wrongRuntime).Principal(); ok {
		t.Fatal("cross-runtime principal was admitted")
	}
}

func TestReportIsBoundedDeterministicAndLastWriterWins(t *testing.T) {
	report := NewReport(
		KindGateway,
		Unavailable(CapabilityBrowserObserve, ReasonPolicyDisabled),
		Available(CapabilityArtifactRead),
		DependencyUnavailable(CapabilityBrowserObserve, CapabilityBrowserClient),
		Availability{Capability: "project.injected", Available: true},
	)
	want := []Availability{
		Available(CapabilityArtifactRead),
		DependencyUnavailable(CapabilityBrowserObserve, CapabilityBrowserClient),
	}
	if !reflect.DeepEqual(report.Capabilities, want) {
		t.Fatalf("report capabilities = %#v, want %#v", report.Capabilities, want)
	}
}

func TestReportJSONOmitsReasonsForAvailableCapabilities(t *testing.T) {
	report := NewAdmissionReport(
		KindGateway,
		[]Availability{
			Available(CapabilityArtifactRead),
			DependencyUnavailable(CapabilityBrowserObserve, CapabilityBrowserClient),
		},
		[]ToolAvailability{
			ToolUnavailable("browser_observe", ReasonPolicyDisabled),
			ToolAvailable("read_file"),
		},
	)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	text := string(encoded)
	if strings.Contains(text, `"capability":"artifact.read","available":true,"reason"`) {
		t.Fatalf("available capability carries a reason: %s", text)
	}
	if !strings.Contains(
		text,
		`"reason":{"code":"dependency_missing","dependency":"browser.client"}`,
	) {
		t.Fatalf("unavailable capability lacks structured reason: %s", text)
	}
	if !strings.Contains(text, `"name":"browser_observe","available":false,"reason":{"code":"policy_disabled"}`) {
		t.Fatalf("denied tool lacks structured reason: %s", text)
	}
	if strings.Contains(text, `"name":"read_file","available":true,"reason"`) {
		t.Fatalf("available tool carries a reason: %s", text)
	}
}

func TestAdmissionReportCanonicalizesAndClonesTools(t *testing.T) {
	report := NewAdmissionReport(
		KindCoding,
		nil,
		[]ToolAvailability{
			ToolUnavailable("write_file", ReasonPolicyDisabled),
			ToolAvailable("read_file"),
			ToolAvailable("write_file"),
			{Name: " invalid ", Available: false},
		},
	)
	if got, want := report.Tools, []ToolAvailability{
		ToolAvailable("read_file"),
		ToolAvailable("write_file"),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("report tools = %#v, want %#v", got, want)
	}
	clone := report.Clone()
	report.Tools[0].Name = "mutated"
	if tool, ok := clone.LookupTool("read_file"); !ok || !tool.Available {
		t.Fatalf("cloned tool availability = %#v, %t", tool, ok)
	}
	if capability, ok := ParseCapabilityID(" Document.Inspect "); !ok ||
		capability != CapabilityDocumentInspect {
		t.Fatalf("ParseCapabilityID() = %q, %t", capability, ok)
	}
	if _, ok := ParseCapabilityID("project.injected"); ok {
		t.Fatal("ParseCapabilityID() admitted an unowned capability")
	}
}

func assertAvailability(
	t *testing.T,
	report Report,
	capability CapabilityID,
	wantAvailable bool,
	wantReason UnavailableReasonCode,
) {
	t.Helper()
	got, ok := report.Lookup(capability)
	if !ok {
		t.Fatalf("report missing %q: %#v", capability, report)
	}
	gotReason := UnavailableReasonCode("")
	if got.Reason != nil {
		gotReason = got.Reason.Code
	}
	if got.Available != wantAvailable || gotReason != wantReason {
		t.Fatalf("%s = %#v, want available=%t reason=%q", capability, got, wantAvailable, wantReason)
	}
}
