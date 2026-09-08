package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

type hookRuntime struct {
	mu          sync.Mutex
	initialized bool
	initErr     error
	mounted     []string
}

func (r *hookRuntime) initialize(load func() ([]string, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.initialized {
		return r.initErr
	}
	r.initialized = true
	mounted, err := load()
	r.initErr = err
	if err == nil {
		r.mounted = append([]string(nil), mounted...)
	}
	return r.initErr
}

func (r *hookRuntime) reset(unmount func(string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range r.mounted {
		if unmount != nil {
			unmount(name)
		}
	}
	r.initialized = false
	r.mounted = nil
	r.initErr = nil
}

// BuiltinHookFactory constructs an in-process hook from config.
type BuiltinHookFactory func(ctx context.Context, spec config.BuiltinHookConfig) (any, error)

var (
	builtinHookRegistryMu sync.RWMutex
	builtinHookRegistry   = map[string]BuiltinHookFactory{}
)

// RegisterBuiltinHook registers a named in-process hook factory for config-driven mounting.
func RegisterBuiltinHook(name string, factory BuiltinHookFactory) error {
	if name == "" {
		return fmt.Errorf("builtin hook name is required")
	}
	if factory == nil {
		return fmt.Errorf("builtin hook %q factory is nil", name)
	}

	builtinHookRegistryMu.Lock()
	defer builtinHookRegistryMu.Unlock()

	if _, exists := builtinHookRegistry[name]; exists {
		return fmt.Errorf("builtin hook %q is already registered", name)
	}
	builtinHookRegistry[name] = factory
	return nil
}

func unregisterBuiltinHook(name string) {
	if name == "" {
		return
	}
	builtinHookRegistryMu.Lock()
	delete(builtinHookRegistry, name)
	builtinHookRegistryMu.Unlock()
}

func lookupBuiltinHook(name string) (BuiltinHookFactory, bool) {
	builtinHookRegistryMu.RLock()
	defer builtinHookRegistryMu.RUnlock()

	factory, ok := builtinHookRegistry[name]
	return factory, ok
}

func configureHookManagerFromConfig(hm *HookManager, cfg *config.Config) {
	if hm == nil || cfg == nil {
		return
	}
	hm.ConfigureTimeouts(
		hookTimeoutFromMS(cfg.Hooks.Defaults.ObserverTimeoutMS),
		hookTimeoutFromMS(cfg.Hooks.Defaults.InterceptorTimeoutMS),
		hookTimeoutFromMS(cfg.Hooks.Defaults.ApprovalTimeoutMS),
	)
}

func hookTimeoutFromMS(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func (al *AgentLoop) ensureHooksInitialized(ctx context.Context) error {
	if al == nil || al.hooks == nil {
		return nil
	}
	if al.usesCodingProfile() {
		return nil
	}
	al.mu.RLock()
	cfg := al.cfg
	al.mu.RUnlock()
	if cfg == nil {
		return nil
	}

	return al.hookRuntime.initialize(func() ([]string, error) {
		return al.loadConfiguredHooks(ctx, cfg)
	})
}

func (al *AgentLoop) loadConfiguredHooks(
	ctx context.Context,
	cfg *config.Config,
) (mounted []string, err error) {
	if al == nil || cfg == nil || !cfg.Hooks.Enabled {
		return nil, nil
	}

	mounted = make([]string, 0)
	defer func() {
		if err != nil {
			for _, name := range mounted {
				al.UnmountHook(name)
			}
			mounted = nil
		}
	}()

	builtinNames := enabledBuiltinHookNames(cfg.Hooks.Builtins)
	for _, name := range builtinNames {
		spec := cfg.Hooks.Builtins[name]
		factory, ok := lookupBuiltinHook(name)
		if !ok {
			return mounted, fmt.Errorf("builtin hook %q is not registered", name)
		}

		hook, factoryErr := factory(ctx, spec)
		if factoryErr != nil {
			return mounted, fmt.Errorf("build builtin hook %q: %w", name, factoryErr)
		}
		if err := al.MountHook(HookRegistration{
			Name:     name,
			Priority: spec.Priority,
			Source:   HookSourceInProcess,
			Hook:     hook,
		}); err != nil {
			return mounted, fmt.Errorf("mount builtin hook %q: %w", name, err)
		}
		mounted = append(mounted, name)
	}

	processNames := enabledProcessHookNames(cfg.Hooks.Processes)
	for _, name := range processNames {
		spec := cfg.Hooks.Processes[name]
		opts, buildErr := processHookOptionsFromConfig(spec)
		if buildErr != nil {
			return mounted, fmt.Errorf("configure process hook %q: %w", name, buildErr)
		}

		processHook, buildErr := NewProcessHook(ctx, name, opts)
		if buildErr != nil {
			return mounted, fmt.Errorf("start process hook %q: %w", name, buildErr)
		}
		if err := al.MountHook(HookRegistration{
			Name:     name,
			Priority: spec.Priority,
			Source:   HookSourceProcess,
			Hook:     processHook,
		}); err != nil {
			_ = processHook.Close()
			return mounted, fmt.Errorf("mount process hook %q: %w", name, err)
		}
		mounted = append(mounted, name)
	}

	return mounted, nil
}

func enabledBuiltinHookNames(specs map[string]config.BuiltinHookConfig) []string {
	if len(specs) == 0 {
		return nil
	}

	names := make([]string, 0, len(specs))
	for name, spec := range specs {
		if spec.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func enabledProcessHookNames(specs map[string]config.ProcessHookConfig) []string {
	if len(specs) == 0 {
		return nil
	}

	names := make([]string, 0, len(specs))
	for name, spec := range specs {
		if spec.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func processHookOptionsFromConfig(spec config.ProcessHookConfig) (ProcessHookOptions, error) {
	transport := spec.Transport
	if transport == "" {
		transport = "stdio"
	}
	if transport != "stdio" {
		return ProcessHookOptions{}, fmt.Errorf("unsupported transport %q", transport)
	}
	if len(spec.Command) == 0 {
		return ProcessHookOptions{}, fmt.Errorf("command is required")
	}

	opts := ProcessHookOptions{
		Command: append([]string(nil), spec.Command...),
		Dir:     spec.Dir,
		Env:     processHookEnvFromMap(spec.Env),
	}

	observeKinds, observeEnabled, err := processHookObserveKindsFromConfig(spec.Observe)
	if err != nil {
		return ProcessHookOptions{}, err
	}
	opts.Observe = observeEnabled
	opts.ObserveKinds = observeKinds

	for _, intercept := range spec.Intercept {
		switch intercept {
		case "before_llm", "after_llm":
			opts.InterceptLLM = true
		case "before_tool", "after_tool":
			opts.InterceptTool = true
		case "approve_tool":
			opts.ApproveTool = true
		case "":
			continue
		default:
			return ProcessHookOptions{}, fmt.Errorf("unsupported intercept %q", intercept)
		}
	}

	if !opts.Observe && !opts.InterceptLLM && !opts.InterceptTool && !opts.ApproveTool {
		return ProcessHookOptions{}, fmt.Errorf("no hook modes enabled")
	}

	return opts, nil
}

func processHookEnvFromMap(envMap map[string]string) []string {
	if len(envMap) == 0 {
		return nil
	}

	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+envMap[key])
	}
	return env
}

func processHookObserveKindsFromConfig(observe []string) ([]string, bool, error) {
	if len(observe) == 0 {
		return nil, false, nil
	}

	validKinds := validHookEventKinds()
	normalized := make([]string, 0, len(observe))
	for _, kind := range observe {
		switch kind {
		case "", "*", "all":
			return nil, true, nil
		default:
			normalizedKind, ok := validKinds[kind]
			if !ok {
				return nil, false, fmt.Errorf("unsupported observe event %q", kind)
			}
			normalized = append(normalized, normalizedKind)
		}
	}

	if len(normalized) == 0 {
		return nil, false, nil
	}
	return normalized, true, nil
}

func validHookEventKinds() map[string]string {
	runtimeKinds := runtimeevents.KnownKinds()
	kinds := make(map[string]string, len(runtimeKinds)*2)
	for _, kind := range runtimeKinds {
		kinds[kind.String()] = kind.String()
	}
	kinds["turn_start"] = runtimeevents.KindAgentTurnStart.String()
	kinds["turn_end"] = runtimeevents.KindAgentTurnEnd.String()
	kinds["llm_request"] = runtimeevents.KindAgentLLMRequest.String()
	kinds["llm_delta"] = runtimeevents.KindAgentLLMDelta.String()
	kinds["llm_response"] = runtimeevents.KindAgentLLMResponse.String()
	kinds["llm_retry"] = runtimeevents.KindAgentLLMRetry.String()
	kinds["llm_fallback_attempt"] = runtimeevents.KindAgentLLMFallbackAttempt.String()
	kinds["context_compress"] = runtimeevents.KindAgentContextCompress.String()
	kinds["context_compress_start"] = runtimeevents.KindAgentContextCompressStart.String()
	kinds["context_compress_progress"] = runtimeevents.KindAgentContextCompressProgress.String()
	kinds["context_compress_end"] = runtimeevents.KindAgentContextCompressEnd.String()
	kinds["context_snapshot"] = runtimeevents.KindAgentContextSnapshot.String()
	kinds["session_summarize"] = runtimeevents.KindAgentSessionSummarize.String()
	kinds["tool_exec_start"] = runtimeevents.KindAgentToolExecStart.String()
	kinds["tool_exec_end"] = runtimeevents.KindAgentToolExecEnd.String()
	kinds["tool_exec_skipped"] = runtimeevents.KindAgentToolExecSkipped.String()
	kinds["tool_loop_decision"] = runtimeevents.KindAgentToolLoopDecision.String()
	kinds["steering_injected"] = runtimeevents.KindAgentSteeringInjected.String()
	kinds["follow_up_queued"] = runtimeevents.KindAgentFollowUpQueued.String()
	kinds["async_completion"] = runtimeevents.KindAgentAsyncCompletion.String()
	kinds["interrupt_received"] = runtimeevents.KindAgentInterruptReceived.String()
	kinds["subturn_spawn"] = runtimeevents.KindAgentSubTurnSpawn.String()
	kinds["subturn_admission"] = runtimeevents.KindAgentSubTurnAdmission.String()
	kinds["subturn_end"] = runtimeevents.KindAgentSubTurnEnd.String()
	kinds["subturn_result_delivered"] = runtimeevents.KindAgentSubTurnResultDelivered.String()
	kinds["subturn_orphan"] = runtimeevents.KindAgentSubTurnOrphan.String()
	kinds["error"] = runtimeevents.KindAgentError.String()
	return kinds
}
