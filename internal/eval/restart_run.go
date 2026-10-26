package eval

import (
	"context"
	"strings"
)

type ownedRestartRun struct {
	child    **ownedGateway
	target   *admittedTarget
	preset   Preset
	bindings map[string]nativeModelBinding
	previous []ownedGatewayGeneration
}

func (run *ownedRestartRun) remember(trial Trial) error {
	bindings, valid := nativeModelBindings(trial, run.preset.ExpectedWorkspace)
	if !valid || *run.child == nil {
		return errNativeEvidence
	}
	if run.bindings == nil {
		run.bindings = map[string]nativeModelBinding{}
	}
	for _, binding := range bindings {
		if binding.Generation == (*run.child).identity.generation {
			run.bindings[binding.hash()] = binding
		}
	}
	return nil
}

func (run *ownedRestartRun) observeModels(
	ctx context.Context, target admittedTarget, generation *ownedGatewayGeneration,
	facts map[string]string, filter *strings.Replacer,
) {
	bindings := []nativeModelBinding{}
	for _, binding := range run.bindings {
		bindings = append(bindings, binding)
	}
	generation.Models = observeNativeModels(
		ctx,
		target,
		&generation.Lifecycle,
		generation.Surfaces,
		bindings,
		facts,
		filter,
	)
}

func (run *ownedRestartRun) next(ctx context.Context, trial Trial) (admittedTarget, int, error) {
	if run.remember(trial) != nil || validateRestartPolicy(run.target.config, run.preset) != nil {
		return admittedTarget{}, 0, errNativeEvidence
	}
	before := *run.target
	next, target, generation, err := restartOwnedGateway(ctx, *run.child, run.preset)
	if generation.Runtime.PID > 1 {
		run.observeModels(ctx, before, &generation, nil, before.config.SensitiveDataReplacer())
		run.previous = append(run.previous, generation)
	}
	if next != nil {
		*run.child = next
	}
	if err != nil || generation.Models == nil || generation.Models.Code != "observed" {
		return admittedTarget{}, 0, errNativeEvidence
	}
	target.priorTraces = map[string]string{}
	for _, previous := range run.previous {
		for _, turn := range previous.Models.Turns {
			target.priorTraces[turn.TraceID] = turn.TraceSHA256
		}
	}
	*run.target = target
	run.bindings = nil
	return target, next.identity.generation, nil
}

func (run *ownedRestartRun) finish(
	ctx context.Context,
	report *RunReport,
	facts map[string]string,
	filter *strings.Replacer,
) {
	if *run.child == nil || report.TargetLifecycle == nil {
		return
	}
	last := ownedGatewayGeneration{
		Number: (*run.child).identity.generation, Runtime: run.target.probe,
		Lifecycle: *report.TargetLifecycle, Budget: report.TargetBudget, Surfaces: report.PrivateSurfaces,
	}
	run.observeModels(ctx, *run.target, &last, facts, filter)
	report.Runtime = last.Runtime
	report.TargetGenerations = append(append([]ownedGatewayGeneration{}, run.previous...), last)
	report.NativeModels = generationModels(report.TargetGenerations)
}
