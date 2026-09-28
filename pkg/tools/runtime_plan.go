package tools

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// RuntimeToolPolicy is evaluated only after every contributor has produced
// its candidate catalog. It therefore cannot hide name collisions between
// independently owned features.
type RuntimeToolPolicy func(name string) bool

// RuntimeToolContributor contributes one feature-owned slice of a runtime
// catalog. Implementations construct candidates but never mutate a live
// ToolRegistry directly.
type RuntimeToolContributor interface {
	Name() string
	Contribute(RuntimeToolContribution) error
}

// RuntimeToolContribution is the bounded view of a RuntimeToolPlan supplied
// to one contributor.
type RuntimeToolContribution struct {
	plan   *runtimeToolPlanState
	source string
}

// Runtime returns the immutable service context selected by the trusted
// composition root.
func (contribution RuntimeToolContribution) Runtime() runtimecap.Context {
	return contribution.plan.runtime
}

// Add adds a tool that is visible to the model without discovery.
func (contribution RuntimeToolContribution) Add(tool toolshared.Tool) error {
	return contribution.plan.add(contribution.source, tool, false)
}

// AddHidden adds a tool that retains the registry's deferred-discovery and
// TTL behavior.
func (contribution RuntimeToolContribution) AddHidden(tool toolshared.Tool) error {
	return contribution.plan.add(contribution.source, tool, true)
}

// Report adds feature-level capability diagnostics to the final report. A
// capability may have one owner; duplicate ownership is rejected.
func (contribution RuntimeToolContribution) Report(entries ...runtimecap.Availability) error {
	return contribution.plan.report(contribution.source, entries)
}

// Provides declares a capability backed by all named candidate tools. The
// final report marks it policy-disabled when any required tool is not admitted.
func (contribution RuntimeToolContribution) Provides(
	capability runtimecap.CapabilityID,
	toolNames ...string,
) error {
	return contribution.plan.provides(contribution.source, capability, toolNames)
}

// RuntimeToolPlan composes a fresh ToolRegistry from feature contributors.
// Registry mutation starts only after every candidate and diagnostic has been
// validated, so a failed contributor or collision cannot publish a partial
// catalog.
type RuntimeToolPlan struct {
	Runtime runtimecap.Context
	Policy  RuntimeToolPolicy
	Seal    bool
}

// RuntimeToolPlanResult is the admitted catalog and its effective capability
// report.
type RuntimeToolPlanResult struct {
	Registry     *ToolRegistry
	Capabilities runtimecap.Report
}

type runtimeToolRegistration struct {
	tool   toolshared.Tool
	hidden bool
}

type runtimeToolPlanState struct {
	runtime          runtimecap.Context
	registrations    []runtimeToolRegistration
	toolOwners       map[string]string
	capabilityOwners map[runtimecap.CapabilityID]string
	requirements     []runtimeToolCapabilityRequirement
	availability     []runtimecap.Availability
}

type runtimeToolCapabilityRequirement struct {
	capability runtimecap.CapabilityID
	toolNames  []string
}

// Build evaluates contributors in the supplied order and returns a new
// registry. ToolRegistry itself still owns execution-time TTL and sealing.
func (plan RuntimeToolPlan) Build(contributors ...RuntimeToolContributor) (RuntimeToolPlanResult, error) {
	switch plan.Runtime.Kind() {
	case runtimecap.KindGateway, runtimecap.KindCoding:
	default:
		return RuntimeToolPlanResult{}, errors.New("runtime tool plan has an invalid runtime")
	}
	state := &runtimeToolPlanState{
		runtime:          plan.Runtime,
		toolOwners:       make(map[string]string),
		capabilityOwners: make(map[runtimecap.CapabilityID]string),
	}
	sources := make(map[string]struct{}, len(contributors))
	for index, contributor := range contributors {
		if interfaceIsNil(contributor) {
			return RuntimeToolPlanResult{}, fmt.Errorf("runtime tool contributor %d is nil", index)
		}
		source := strings.TrimSpace(contributor.Name())
		if source == "" || source != contributor.Name() {
			return RuntimeToolPlanResult{}, fmt.Errorf("runtime tool contributor %d has an invalid name", index)
		}
		if _, duplicate := sources[source]; duplicate {
			return RuntimeToolPlanResult{}, fmt.Errorf("duplicate runtime tool contributor %q", source)
		}
		sources[source] = struct{}{}
		if err := contributor.Contribute(RuntimeToolContribution{plan: state, source: source}); err != nil {
			return RuntimeToolPlanResult{}, fmt.Errorf("runtime tool contributor %q: %w", source, err)
		}
	}

	admitted := make(map[string]struct{}, len(state.registrations))
	for _, registration := range state.registrations {
		if plan.Policy == nil || plan.Policy(registration.tool.Name()) {
			admitted[registration.tool.Name()] = struct{}{}
		}
	}
	availability := append([]runtimecap.Availability(nil), plan.Runtime.Report().Capabilities...)
	availability = append(availability, state.availability...)
	for _, requirement := range state.requirements {
		entry := runtimecap.Available(requirement.capability)
		for _, toolName := range requirement.toolNames {
			if _, candidate := state.toolOwners[toolName]; !candidate {
				return RuntimeToolPlanResult{}, fmt.Errorf(
					"runtime capability %q requires unknown tool %q",
					requirement.capability,
					toolName,
				)
			}
			if _, ok := admitted[toolName]; !ok {
				entry = runtimecap.Unavailable(requirement.capability, runtimecap.ReasonPolicyDisabled)
			}
		}
		availability = append(availability, entry)
	}

	registry := NewToolRegistry()
	for _, registration := range state.registrations {
		if _, ok := admitted[registration.tool.Name()]; !ok {
			continue
		}
		if registration.hidden {
			registry.RegisterHidden(registration.tool)
		} else {
			registry.Register(registration.tool)
		}
	}
	if plan.Seal {
		registry.Seal()
	}

	return RuntimeToolPlanResult{
		Registry:     registry,
		Capabilities: runtimecap.NewReport(plan.Runtime.Kind(), availability...),
	}, nil
}

func (plan *runtimeToolPlanState) add(source string, tool toolshared.Tool, hidden bool) error {
	if interfaceIsNil(tool) {
		return errors.New("runtime tool is nil")
	}
	name := strings.TrimSpace(tool.Name())
	if name == "" || name != tool.Name() {
		return errors.New("runtime tool has an invalid name")
	}
	if owner, duplicate := plan.toolOwners[name]; duplicate {
		return fmt.Errorf("runtime tool %q from %q collides with contributor %q", name, source, owner)
	}
	plan.toolOwners[name] = source
	plan.registrations = append(plan.registrations, runtimeToolRegistration{tool: tool, hidden: hidden})
	return nil
}

func (plan *runtimeToolPlanState) provides(
	source string,
	capability runtimecap.CapabilityID,
	toolNames []string,
) error {
	if err := plan.claimCapability(source, runtimecap.Available(capability)); err != nil {
		return err
	}
	if len(toolNames) == 0 {
		return fmt.Errorf("runtime capability %q has no required tools", capability)
	}
	normalized := make([]string, 0, len(toolNames))
	seen := make(map[string]struct{}, len(toolNames))
	for _, raw := range toolNames {
		name := strings.TrimSpace(raw)
		if name == "" || name != raw {
			return fmt.Errorf("runtime capability %q has an invalid required tool", capability)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("runtime capability %q repeats required tool %q", capability, name)
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	plan.requirements = append(plan.requirements, runtimeToolCapabilityRequirement{
		capability: capability,
		toolNames:  normalized,
	})
	return nil
}

func (plan *runtimeToolPlanState) report(source string, entries []runtimecap.Availability) error {
	for _, entry := range entries {
		if err := plan.claimCapability(source, entry); err != nil {
			return err
		}
		normalized := runtimecap.NewReport(plan.runtime.Kind(), entry)
		plan.availability = append(plan.availability, normalized.Capabilities[0])
	}
	return nil
}

func (plan *runtimeToolPlanState) claimCapability(source string, entry runtimecap.Availability) error {
	normalized := runtimecap.NewReport(plan.runtime.Kind(), entry)
	if len(normalized.Capabilities) != 1 || normalized.Capabilities[0].Capability != entry.Capability {
		return fmt.Errorf("runtime tool contributor %q reported an invalid capability", source)
	}
	if owner, duplicate := plan.capabilityOwners[entry.Capability]; duplicate {
		return fmt.Errorf(
			"runtime capability %q from %q collides with contributor %q",
			entry.Capability,
			source,
			owner,
		)
	}
	plan.capabilityOwners[entry.Capability] = source
	return nil
}

func interfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
