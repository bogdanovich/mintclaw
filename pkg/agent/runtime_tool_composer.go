package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type runtimeToolCandidate struct {
	tool   toolshared.Tool
	hidden bool
}

type runtimeToolUpdate struct {
	source string
	runtimeToolCandidate
}

var runtimeToolComposerSequence atomic.Uint64

// runtimeToolSetContributor retains already-constructed tool instances. A
// composer may rebuild the catalog many times as late-bound features arrive;
// constructors with processes, stores, or other lifecycle state must not run
// again merely because another contributor changed.
type runtimeToolSetContributor struct {
	name         string
	candidates   []runtimeToolCandidate
	reports      []runtimecap.Availability
	capabilities []runtimeToolCapability
}

type runtimeToolCapability struct {
	capability runtimecap.CapabilityID
	toolNames  []string
}

func (contributor runtimeToolSetContributor) Name() string {
	return contributor.name
}

func (contributor runtimeToolSetContributor) Contribute(plan tools.RuntimeToolContribution) error {
	for _, candidate := range contributor.candidates {
		var err error
		if candidate.hidden {
			err = plan.AddHidden(candidate.tool)
		} else {
			err = plan.Add(candidate.tool)
		}
		if err != nil {
			return err
		}
	}
	if err := plan.Report(contributor.reports...); err != nil {
		return err
	}
	for _, provided := range contributor.capabilities {
		if err := plan.Provides(provided.capability, provided.toolNames...); err != nil {
			return err
		}
	}
	return nil
}

func newRuntimeToolSetContributor(
	name string,
	candidates ...runtimeToolCandidate,
) runtimeToolSetContributor {
	return runtimeToolSetContributor{
		name:       name,
		candidates: append([]runtimeToolCandidate(nil), candidates...),
	}
}

func (contributor runtimeToolSetContributor) withCapability(
	capability runtimecap.CapabilityID,
	toolNames ...string,
) runtimeToolSetContributor {
	contributor.capabilities = append(
		append([]runtimeToolCapability(nil), contributor.capabilities...),
		runtimeToolCapability{
			capability: capability,
			toolNames:  append([]string(nil), toolNames...),
		},
	)
	return contributor
}

func (contributor runtimeToolSetContributor) withCapabilityReport(
	reports ...runtimecap.Availability,
) runtimeToolSetContributor {
	contributor.reports = append(
		append([]runtimecap.Availability(nil), contributor.reports...),
		reports...,
	)
	return contributor
}

// runtimeToolComposer owns one stable registry and republishes it only after
// RuntimeToolPlan has validated the complete candidate catalog. Gateway
// services and coding-thread features therefore share composition semantics
// without sharing an admission policy or a runtime lifecycle.
type runtimeToolComposer struct {
	mu           sync.Mutex
	sequence     uint64
	plan         tools.RuntimeToolPlan
	registry     *tools.ToolRegistry
	contributors map[string]runtimeToolSetContributor
	order        []string
	capabilities runtimecap.Report
}

type runtimeToolComposerDraft struct {
	contributors map[string]runtimeToolSetContributor
	order        []string
}

type runtimeToolComposerUpdate struct {
	composer *runtimeToolComposer
	label    string
	mutate   func(*runtimeToolComposerDraft) error
}

type preparedRuntimeToolComposerUpdate struct {
	composer *runtimeToolComposer
	draft    runtimeToolComposerDraft
	result   tools.RuntimeToolPlanResult
}

func newRuntimeToolComposer(
	runtime runtimecap.Context,
	policy tools.RuntimeToolPolicy,
	contributors ...runtimeToolSetContributor,
) (*runtimeToolComposer, error) {
	composer := &runtimeToolComposer{
		sequence: runtimeToolComposerSequence.Add(1),
		plan: tools.RuntimeToolPlan{
			Runtime: runtime,
			Policy:  policy,
		},
		contributors: make(map[string]runtimeToolSetContributor, len(contributors)),
	}
	for _, contributor := range contributors {
		name := strings.TrimSpace(contributor.Name())
		if name == "" || name != contributor.Name() {
			return nil, fmt.Errorf("runtime tool contributor has an invalid name")
		}
		if _, duplicate := composer.contributors[name]; duplicate {
			return nil, fmt.Errorf("duplicate runtime tool contributor %q", name)
		}
		composer.contributors[name] = contributor
		composer.order = append(composer.order, name)
	}
	result, err := composer.build(composer.contributors, composer.order)
	if err != nil {
		return nil, err
	}
	composer.registry = result.Registry
	composer.capabilities = result.Capabilities
	return composer, nil
}

func (composer *runtimeToolComposer) Registry() *tools.ToolRegistry {
	if composer == nil {
		return nil
	}
	composer.mu.Lock()
	defer composer.mu.Unlock()
	return composer.registry
}

func (composer *runtimeToolComposer) CapabilityReport() runtimecap.Report {
	if composer == nil {
		return runtimecap.Report{}
	}
	composer.mu.Lock()
	defer composer.mu.Unlock()
	return composer.capabilities.Clone()
}

func (composer *runtimeToolComposer) PutTool(
	source string,
	tool toolshared.Tool,
	hidden bool,
) error {
	return composer.PutTools(runtimeToolUpdate{
		source: source,
		runtimeToolCandidate: runtimeToolCandidate{
			tool:   tool,
			hidden: hidden,
		},
	})
}

func (composer *runtimeToolComposer) PutTools(updates ...runtimeToolUpdate) error {
	if composer == nil {
		return fmt.Errorf("runtime tool composer is nil")
	}
	if len(updates) == 0 {
		return nil
	}
	return applyRuntimeToolComposerUpdates(runtimeToolComposerUpdate{
		composer: composer,
		label:    "runtime tool batch",
		mutate: func(draft *runtimeToolComposerDraft) error {
			for _, update := range updates {
				if err := draft.put(update.source, update.runtimeToolCandidate); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

func (composer *runtimeToolComposer) Remove(source string) error {
	if composer == nil {
		return fmt.Errorf("runtime tool composer is nil")
	}
	return applyRuntimeToolComposerUpdates(runtimeToolComposerUpdate{
		composer: composer,
		label:    "runtime tool removal",
		mutate: func(draft *runtimeToolComposerDraft) error {
			return draft.remove(source)
		},
	})
}

func (composer *runtimeToolComposer) ReplaceTool(name string, replacement toolshared.Tool) error {
	if composer == nil {
		return fmt.Errorf("runtime tool composer is nil")
	}
	name = strings.TrimSpace(name)
	if replacement == nil || replacement.Name() != name {
		return fmt.Errorf("replacement for runtime tool %q is invalid", name)
	}

	return applyRuntimeToolComposerUpdates(runtimeToolComposerUpdate{
		composer: composer,
		label:    "runtime tool replacement",
		mutate: func(draft *runtimeToolComposerDraft) error {
			return draft.replace(name, replacement)
		},
	})
}

func (draft *runtimeToolComposerDraft) put(source string, candidate runtimeToolCandidate) error {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" || trimmed != source {
		return fmt.Errorf("runtime tool contributor name is required")
	}
	if _, exists := draft.contributors[source]; !exists {
		draft.order = append(draft.order, source)
	}
	draft.contributors[source] = newRuntimeToolSetContributor(source, candidate)
	return nil
}

func (draft *runtimeToolComposerDraft) remove(source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("runtime tool contributor name is required")
	}
	if _, ok := draft.contributors[source]; !ok {
		return nil
	}
	delete(draft.contributors, source)
	nextOrder := make([]string, 0, len(draft.order)-1)
	for _, name := range draft.order {
		if name != source {
			nextOrder = append(nextOrder, name)
		}
	}
	draft.order = nextOrder
	return nil
}

func (draft *runtimeToolComposerDraft) replace(name string, replacement toolshared.Tool) error {
	name = strings.TrimSpace(name)
	if replacement == nil || replacement.Name() != name {
		return fmt.Errorf("replacement for runtime tool %q is invalid", name)
	}
	found := false
	for source, contributor := range draft.contributors {
		for index, candidate := range contributor.candidates {
			if candidate.tool == nil || candidate.tool.Name() != name {
				continue
			}
			candidate.tool = replacement
			contributor.candidates[index] = candidate
			draft.contributors[source] = contributor
			found = true
		}
	}
	if !found {
		return fmt.Errorf("runtime tool %q is not registered", name)
	}
	return nil
}

func applyRuntimeToolComposerUpdates(updates ...runtimeToolComposerUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	ordered := append([]runtimeToolComposerUpdate(nil), updates...)
	for index, update := range ordered {
		if update.composer == nil {
			return fmt.Errorf("runtime tool composer update %d has no composer", index)
		}
		if update.mutate == nil {
			return fmt.Errorf("runtime tool composer update %d has no mutation", index)
		}
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].composer.sequence < ordered[right].composer.sequence
	})
	for index := range ordered {
		if index > 0 && ordered[index-1].composer == ordered[index].composer {
			return fmt.Errorf("runtime tool composer has multiple updates in one transaction")
		}
	}
	for index := range ordered {
		ordered[index].composer.mu.Lock()
	}
	defer func() {
		for index := len(ordered) - 1; index >= 0; index-- {
			ordered[index].composer.mu.Unlock()
		}
	}()

	prepared := make([]preparedRuntimeToolComposerUpdate, 0, len(ordered))
	for _, update := range ordered {
		composer := update.composer
		draft := runtimeToolComposerDraft{
			contributors: cloneRuntimeToolContributors(composer.contributors),
			order:        append([]string(nil), composer.order...),
		}
		if err := update.mutate(&draft); err != nil {
			return fmt.Errorf("%s: %w", update.label, err)
		}
		result, err := composer.build(draft.contributors, draft.order)
		if err != nil {
			return fmt.Errorf("%s: %w", update.label, err)
		}
		if err = composer.registry.ValidateReplaceFrom(result.Registry); err != nil {
			return fmt.Errorf("%s: %w", update.label, err)
		}
		prepared = append(prepared, preparedRuntimeToolComposerUpdate{
			composer: composer,
			draft:    draft,
			result:   result,
		})
	}

	replacements := make([]tools.ToolRegistryReplacement, 0, len(prepared))
	for _, update := range prepared {
		replacements = append(replacements, tools.ToolRegistryReplacement{
			Target: update.composer.registry, Candidate: update.result.Registry,
		})
	}
	if err := tools.ReplaceToolRegistries(replacements, func() {
		for _, update := range prepared {
			update.composer.contributors = update.draft.contributors
			update.composer.order = append([]string(nil), update.draft.order...)
			update.composer.capabilities = update.result.Capabilities
		}
	}); err != nil {
		return err
	}
	return nil
}

func (composer *runtimeToolComposer) build(
	contributors map[string]runtimeToolSetContributor,
	order []string,
) (tools.RuntimeToolPlanResult, error) {
	ordered := make([]tools.RuntimeToolContributor, 0, len(order))
	for _, name := range order {
		contributor, ok := contributors[name]
		if !ok {
			return tools.RuntimeToolPlanResult{}, fmt.Errorf("runtime tool contributor %q is missing", name)
		}
		ordered = append(ordered, contributor)
	}
	return composer.plan.Build(ordered...)
}

func cloneRuntimeToolContributors(
	contributors map[string]runtimeToolSetContributor,
) map[string]runtimeToolSetContributor {
	clone := make(map[string]runtimeToolSetContributor, len(contributors))
	for name, contributor := range contributors {
		contributor.candidates = append([]runtimeToolCandidate(nil), contributor.candidates...)
		contributor.reports = append([]runtimecap.Availability(nil), contributor.reports...)
		capabilities := make([]runtimeToolCapability, len(contributor.capabilities))
		for index, capability := range contributor.capabilities {
			capabilities[index] = runtimeToolCapability{
				capability: capability.capability,
				toolNames:  append([]string(nil), capability.toolNames...),
			}
		}
		contributor.capabilities = capabilities
		clone[name] = contributor
	}
	return clone
}
