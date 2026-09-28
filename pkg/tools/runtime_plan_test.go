package tools

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

type runtimeToolContributorFunc struct {
	name       string
	contribute func(RuntimeToolContribution) error
}

func (contributor runtimeToolContributorFunc) Name() string {
	return contributor.name
}

func (contributor runtimeToolContributorFunc) Contribute(contribution RuntimeToolContribution) error {
	return contributor.contribute(contribution)
}

func TestRuntimeToolPlanBuildsPolicyFilteredCatalogAndReport(t *testing.T) {
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding})
	plan := RuntimeToolPlan{
		Runtime: runtime,
		Policy:  func(name string) bool { return name != "deferred" },
		Seal:    true,
	}
	result, err := plan.Build(runtimeToolContributorFunc{
		name: "test.feature",
		contribute: func(contribution RuntimeToolContribution) error {
			if contribution.Runtime().Kind() != runtimecap.KindCoding {
				t.Fatal("contributor received the wrong runtime")
			}
			if err := contribution.Add(newMockTool("visible", "visible")); err != nil {
				return err
			}
			if err := contribution.AddHidden(newMockTool("deferred", "deferred")); err != nil {
				return err
			}
			if err := contribution.Provides(runtimecap.CapabilityDocumentInspect, "visible"); err != nil {
				return err
			}
			return contribution.Provides(runtimecap.CapabilityDocumentExtract, "deferred")
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := result.Registry.List(), []string{"visible"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("registry tools = %v, want %v", got, want)
	}
	availability, ok := result.Capabilities.Lookup(runtimecap.CapabilityDocumentInspect)
	if !ok || !availability.Available {
		t.Fatalf("document.inspect availability = %#v, %t", availability, ok)
	}
	extract, ok := result.Capabilities.Lookup(runtimecap.CapabilityDocumentExtract)
	if !ok || extract.Available || extract.Reason == nil || extract.Reason.Code != runtimecap.ReasonPolicyDisabled {
		t.Fatalf("document.extract availability = %#v, %t", extract, ok)
	}
	result.Registry.Register(newMockTool("late", "late"))
	if result.Registry.HasRegistered("late") {
		t.Fatal("sealed runtime plan admitted a late tool")
	}
}

func TestRuntimeToolPlanPreservesHiddenDiscoveryState(t *testing.T) {
	result, err := (RuntimeToolPlan{
		Runtime: runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway}),
	}).Build(runtimeToolContributorFunc{
		name: "test.discovery",
		contribute: func(contribution RuntimeToolContribution) error {
			return contribution.AddHidden(newMockTool("deferred", "deferred"))
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !result.Registry.HasRegistered("deferred") {
		t.Fatal("hidden tool was not registered")
	}
	if _, visible := result.Registry.Get("deferred"); visible {
		t.Fatal("hidden tool was visible before promotion")
	}
	result.Registry.PromoteTools([]string{"deferred"}, 1)
	if _, visible := result.Registry.Get("deferred"); !visible {
		t.Fatal("hidden tool was not visible after promotion")
	}
	result.Registry.TickTTL()
	if _, visible := result.Registry.Get("deferred"); visible {
		t.Fatal("hidden tool remained visible after TTL expiry")
	}
}

func TestRuntimeToolPlanRejectsCollisionBeforePublishingRegistry(t *testing.T) {
	plan := RuntimeToolPlan{
		Runtime: runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
		Policy:  func(string) bool { return false },
	}
	result, err := plan.Build(
		runtimeToolContributorFunc{
			name: "first",
			contribute: func(contribution RuntimeToolContribution) error {
				return contribution.Add(newMockTool("same", "first"))
			},
		},
		runtimeToolContributorFunc{
			name: "second",
			contribute: func(contribution RuntimeToolContribution) error {
				return contribution.AddHidden(newMockTool("same", "second"))
			},
		},
	)
	want := `runtime tool "same" from "second" collides with contributor "first"`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Build() error = %v, want deterministic collision", err)
	}
	if result.Registry != nil {
		t.Fatal("collision published a partial registry")
	}
}

func TestRuntimeToolPlanAppliesDifferentPoliciesToSameContributors(t *testing.T) {
	contributor := runtimeToolContributorFunc{
		name: "shared.feature",
		contribute: func(contribution RuntimeToolContribution) error {
			if err := contribution.Add(newMockTool("gateway_only", "gateway")); err != nil {
				return err
			}
			return contribution.Add(newMockTool("coding_only", "coding"))
		},
	}
	for _, test := range []struct {
		name    string
		runtime runtimecap.Kind
		allow   string
	}{
		{name: "gateway", runtime: runtimecap.KindGateway, allow: "gateway_only"},
		{name: "coding", runtime: runtimecap.KindCoding, allow: "coding_only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := (RuntimeToolPlan{
				Runtime: runtimecap.NewContext(runtimecap.Inputs{Kind: test.runtime}),
				Policy:  func(name string) bool { return name == test.allow },
			}).Build(contributor)
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if got, want := result.Registry.List(), []string{test.allow}; !reflect.DeepEqual(got, want) {
				t.Fatalf("registry tools = %v, want %v", got, want)
			}
		})
	}
}

func TestRuntimeToolPlanRejectsFailedContributorBeforePublishingRegistry(t *testing.T) {
	wantErr := errors.New("injected failure")
	result, err := (RuntimeToolPlan{
		Runtime: runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
	}).Build(runtimeToolContributorFunc{
		name: "failing",
		contribute: func(contribution RuntimeToolContribution) error {
			if addErr := contribution.Add(newMockTool("candidate", "candidate")); addErr != nil {
				return addErr
			}
			return wantErr
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Build() error = %v, want %v", err, wantErr)
	}
	if result.Registry != nil {
		t.Fatal("failed contributor published a partial registry")
	}
}

func TestRuntimeToolPlanRejectsCapabilityWithUnknownRequiredTool(t *testing.T) {
	result, err := (RuntimeToolPlan{
		Runtime: runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
	}).Build(runtimeToolContributorFunc{
		name: "broken.feature",
		contribute: func(contribution RuntimeToolContribution) error {
			return contribution.Provides(runtimecap.CapabilityDocumentRender, "missing")
		},
	})
	want := `runtime capability "document.render" requires unknown tool "missing"`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Build() error = %v, want unknown required tool", err)
	}
	if result.Registry != nil {
		t.Fatal("invalid capability requirement published a partial registry")
	}
}

func TestRuntimeToolPlanRejectsInvalidRuntimeBeforeCallingContributors(t *testing.T) {
	called := false
	result, err := (RuntimeToolPlan{}).Build(runtimeToolContributorFunc{
		name: "unexpected",
		contribute: func(RuntimeToolContribution) error {
			called = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid runtime") {
		t.Fatalf("Build() error = %v, want invalid runtime", err)
	}
	if called || result.Registry != nil {
		t.Fatal("invalid runtime evaluated contributors or published a registry")
	}
}
