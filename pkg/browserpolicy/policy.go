package browserpolicy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/bogdanovich/mintclaw/pkg/browseraction"
)

const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
	DecisionAsk   = "ask"

	MaxPolicyRules        = 64
	MaxPolicyMatchValues  = 32
	MaxPolicyIDBytes      = 64
	MaxPolicyPatternBytes = 256
	MaxHookArguments      = 64
	MaxHookArgumentBytes  = 4096
	MaxHookTimeoutMS      = 30_000
	MaxHookOutputBytes    = 4096
	MaxHookMessageBytes   = 512
	hookPipeWaitDelay     = 100 * time.Millisecond
)

var (
	ErrInvalidPolicy  = errors.New("invalid browser policy")
	ErrPolicyHook     = errors.New("browser policy hook failed closed")
	policyIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	policyRolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

type Policy struct {
	DefaultDecision string `json:"default_decision"`
	Rules           []Rule `json:"rules,omitempty"`
	Hook            *Hook  `json:"hook,omitempty"`
}

type Rule struct {
	ID       string    `json:"id"`
	Match    RuleMatch `json:"match"`
	Decision string    `json:"decision"`
}

type RuleMatch struct {
	Actions      []string `json:"actions,omitempty"`
	Effects      []string `json:"effects,omitempty"`
	Origins      []string `json:"origins,omitempty"`
	Roles        []string `json:"roles,omitempty"`
	NamePatterns []string `json:"name_patterns,omitempty"`
}

type Hook struct {
	Command   []string `json:"command"`
	TimeoutMS int      `json:"timeout_ms"`
}

// ActionMetadata is the complete privacy-safe policy input. It deliberately
// has no field for text input, cookies, page content, credentials, or artifact
// bytes.
type ActionMetadata struct {
	Action          string `json:"action"`
	Effect          string `json:"effect"`
	Origin          string `json:"origin"`
	Role            string `json:"role,omitempty"`
	Name            string `json:"name,omitempty"`
	ProfileRevision string `json:"profile_revision"`
	PolicyRevision  string `json:"policy_revision"`
}

type Result struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

type hookInput struct {
	SchemaVersion   string `json:"schema_version"`
	ProfileRevision string `json:"profile_revision"`
	PolicyRevision  string `json:"policy_revision"`
	Action          struct {
		Kind   string `json:"kind"`
		Effect string `json:"effect"`
		Origin string `json:"origin"`
		Role   string `json:"role,omitempty"`
		Name   string `json:"name,omitempty"`
	} `json:"action"`
}

func DecisionValid(decision string) bool {
	switch decision {
	case DecisionAllow, DecisionDeny, DecisionAsk:
		return true
	default:
		return false
	}
}

func NormalizePolicy(policy Policy) (Policy, error) {
	if !DecisionValid(policy.DefaultDecision) || len(policy.Rules) > MaxPolicyRules {
		return Policy{}, ErrInvalidPolicy
	}
	normalized := Policy{DefaultDecision: policy.DefaultDecision}
	normalized.Rules = make([]Rule, len(policy.Rules))
	seenIDs := make(map[string]struct{}, len(policy.Rules))
	for index, rule := range policy.Rules {
		if !policyIDPattern.MatchString(rule.ID) || !DecisionValid(rule.Decision) {
			return Policy{}, ErrInvalidPolicy
		}
		if _, exists := seenIDs[rule.ID]; exists {
			return Policy{}, ErrInvalidPolicy
		}
		seenIDs[rule.ID] = struct{}{}
		match, err := normalizeRuleMatch(rule.Match)
		if err != nil {
			return Policy{}, err
		}
		normalized.Rules[index] = Rule{ID: rule.ID, Match: match, Decision: rule.Decision}
	}
	if policy.Hook != nil {
		hook, err := normalizeHook(*policy.Hook)
		if err != nil {
			return Policy{}, err
		}
		normalized.Hook = &hook
	}
	return normalized, nil
}

func ClonePolicy(policy *Policy) *Policy {
	if policy == nil {
		return nil
	}
	cloned := *policy
	cloned.Rules = make([]Rule, len(policy.Rules))
	for index, rule := range policy.Rules {
		cloned.Rules[index] = rule
		cloned.Rules[index].Match.Actions = append([]string(nil), rule.Match.Actions...)
		cloned.Rules[index].Match.Effects = append([]string(nil), rule.Match.Effects...)
		cloned.Rules[index].Match.Origins = append([]string(nil), rule.Match.Origins...)
		cloned.Rules[index].Match.Roles = append([]string(nil), rule.Match.Roles...)
		cloned.Rules[index].Match.NamePatterns = append([]string(nil), rule.Match.NamePatterns...)
	}
	if policy.Hook != nil {
		hook := *policy.Hook
		hook.Command = append([]string(nil), policy.Hook.Command...)
		cloned.Hook = &hook
	}
	return &cloned
}

func PolicyRevision(policy Policy) (string, error) {
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode browser policy: %w", err)
	}
	digest := sha256.Sum256(append([]byte("mintclaw.browser.policy.v1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

func Evaluate(ctx context.Context, policy Policy, metadata ActionMetadata) (Result, error) {
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		return Result{Decision: DecisionDeny}, err
	}
	if err = validateActionMetadata(metadata); err != nil {
		return Result{Decision: DecisionDeny}, err
	}
	revision, err := PolicyRevision(normalized)
	if err != nil || metadata.PolicyRevision != revision {
		return Result{Decision: DecisionDeny}, ErrInvalidPolicy
	}
	result := evaluateDeclarative(normalized, metadata)
	if result.Decision == DecisionDeny || normalized.Hook == nil {
		return result, nil
	}
	hookResult, err := evaluateHook(ctx, *normalized.Hook, metadata)
	if err != nil {
		return Result{Decision: DecisionDeny, Reason: "policy_hook_failed"}, err
	}
	return hookResult, nil
}

func CombineDecisions(left, right string) (string, error) {
	if !DecisionValid(left) || !DecisionValid(right) {
		return DecisionDeny, ErrInvalidPolicy
	}
	if left == DecisionDeny || right == DecisionDeny {
		return DecisionDeny, nil
	}
	if left == DecisionAsk || right == DecisionAsk {
		return DecisionAsk, nil
	}
	return DecisionAllow, nil
}

func evaluateDeclarative(policy Policy, metadata ActionMetadata) Result {
	for _, rule := range policy.Rules {
		if ruleMatches(rule.Match, metadata) {
			return Result{Decision: rule.Decision}
		}
	}
	return Result{Decision: policy.DefaultDecision}
}

func ruleMatches(match RuleMatch, metadata ActionMetadata) bool {
	if len(match.Actions) != 0 && !slices.Contains(match.Actions, metadata.Action) {
		return false
	}
	if len(match.Effects) != 0 && !slices.Contains(match.Effects, metadata.Effect) {
		return false
	}
	if len(match.Origins) != 0 && !slices.Contains(match.Origins, metadata.Origin) {
		return false
	}
	if len(match.Roles) != 0 && !slices.Contains(match.Roles, normalizePolicyText(metadata.Role)) {
		return false
	}
	if len(match.NamePatterns) == 0 {
		return true
	}
	name := normalizePolicyText(metadata.Name)
	for _, pattern := range match.NamePatterns {
		if wildcardMatch(pattern, name) {
			return true
		}
	}
	return false
}

func normalizeRuleMatch(match RuleMatch) (RuleMatch, error) {
	var err error
	if match.Actions, err = normalizeMatchValues(match.Actions, func(value string) (string, bool) {
		return value, browseraction.ActionKind(value).Valid()
	}); err != nil {
		return RuleMatch{}, err
	}
	if match.Effects, err = normalizeMatchValues(match.Effects, func(value string) (string, bool) {
		return value, effectValid(value)
	}); err != nil {
		return RuleMatch{}, err
	}
	if match.Origins, err = normalizeMatchValues(match.Origins, func(value string) (string, bool) {
		normalized, normalizeErr := NormalizeHTTPOrigin(value)
		return normalized, normalizeErr == nil
	}); err != nil {
		return RuleMatch{}, err
	}
	if match.Roles, err = normalizeMatchValues(match.Roles, func(value string) (string, bool) {
		normalized := normalizePolicyText(value)
		return normalized, policyRolePattern.MatchString(normalized)
	}); err != nil {
		return RuleMatch{}, err
	}
	if match.NamePatterns, err = normalizeMatchValues(match.NamePatterns, func(value string) (string, bool) {
		normalized := normalizePolicyText(value)
		return normalized, normalized != "" && len(normalized) <= MaxPolicyPatternBytes &&
			!strings.ContainsRune(normalized, 0) && !containsControl(normalized)
	}); err != nil {
		return RuleMatch{}, err
	}
	return match, nil
}

func normalizeMatchValues(
	values []string,
	normalize func(string) (string, bool),
) ([]string, error) {
	if len(values) > MaxPolicyMatchValues {
		return nil, ErrInvalidPolicy
	}
	result := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		normalized, ok := normalize(value)
		if !ok || normalized == "" {
			return nil, ErrInvalidPolicy
		}
		if _, duplicate := seen[normalized]; duplicate {
			return nil, ErrInvalidPolicy
		}
		seen[normalized] = struct{}{}
		result[index] = normalized
	}
	slices.Sort(result)
	return result, nil
}

func normalizeHook(hook Hook) (Hook, error) {
	if len(hook.Command) == 0 || len(hook.Command) > MaxHookArguments ||
		hook.TimeoutMS <= 0 || hook.TimeoutMS > MaxHookTimeoutMS {
		return Hook{}, ErrInvalidPolicy
	}
	normalized := Hook{Command: append([]string(nil), hook.Command...), TimeoutMS: hook.TimeoutMS}
	for _, argument := range normalized.Command {
		if argument == "" || len(argument) > MaxHookArgumentBytes || strings.ContainsRune(argument, 0) {
			return Hook{}, ErrInvalidPolicy
		}
	}
	if !filepath.IsAbs(normalized.Command[0]) {
		return Hook{}, ErrInvalidPolicy
	}
	return normalized, nil
}

func validateActionMetadata(metadata ActionMetadata) error {
	if !browseraction.ActionKind(metadata.Action).Valid() || !effectValid(metadata.Effect) ||
		metadata.Origin == "" || metadata.ProfileRevision == "" || metadata.PolicyRevision == "" ||
		len(metadata.Role) > MaxPolicyPatternBytes || len(metadata.Name) > MaxPolicyPatternBytes*2 ||
		strings.ContainsRune(metadata.Role, 0) || strings.ContainsRune(metadata.Name, 0) {
		return ErrInvalidPolicy
	}
	normalizedOrigin, err := NormalizeHTTPOrigin(metadata.Origin)
	if err != nil || normalizedOrigin != metadata.Origin {
		return ErrInvalidPolicy
	}
	return nil
}

func effectValid(effect string) bool {
	switch effect {
	case "read", "navigation", "local_edit", "external_commit", "unknown":
		return true
	default:
		return false
	}
}

func evaluateHook(ctx context.Context, hook Hook, metadata ActionMetadata) (Result, error) {
	input := hookInput{
		SchemaVersion:   "mintclaw.browser.policy.input.v1",
		ProfileRevision: metadata.ProfileRevision,
		PolicyRevision:  metadata.PolicyRevision,
	}
	input.Action.Kind = metadata.Action
	input.Action.Effect = metadata.Effect
	input.Action.Origin = metadata.Origin
	input.Action.Role = metadata.Role
	input.Action.Name = metadata.Name
	encoded, err := json.Marshal(input)
	if err != nil {
		return Result{}, errors.Join(ErrPolicyHook, err)
	}
	hookCtx, cancel := context.WithTimeout(ctx, time.Duration(hook.TimeoutMS)*time.Millisecond)
	defer cancel()
	command := exec.CommandContext(hookCtx, hook.Command[0], hook.Command[1:]...)
	command.Stdin = bytes.NewReader(encoded)
	// Hooks are operator-owned policy programs, but the gateway and companion
	// may carry unrelated credentials in their process environments. Do not
	// grant those credentials implicitly. Absolute argv remains the only
	// operator-configured execution authority.
	command.Env = minimalHookEnvironment()
	command.Stderr = io.Discard
	command.WaitDelay = hookPipeWaitDelay
	var output boundedHookOutput
	command.Stdout = &output
	runErr := command.Run()
	if hookCtx.Err() != nil || runErr != nil || output.tooLarge {
		return Result{}, errors.Join(ErrPolicyHook, hookCtx.Err(), runErr)
	}
	var result Result
	decoder := json.NewDecoder(bytes.NewReader(output.data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return Result{}, errors.Join(ErrPolicyHook, err)
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !DecisionValid(result.Decision) ||
		len(result.Reason) > MaxHookMessageBytes || len(result.Summary) > MaxHookMessageBytes ||
		strings.ContainsRune(result.Reason, 0) || strings.ContainsRune(result.Summary, 0) ||
		containsControl(result.Reason) || containsControl(result.Summary) {
		return Result{}, errors.Join(ErrPolicyHook, err)
	}
	return result, nil
}

func minimalHookEnvironment() []string {
	if runtime.GOOS == "windows" {
		systemRoot := os.Getenv("SystemRoot")
		if systemRoot == "" {
			systemRoot = `C:\Windows`
		}
		return []string{
			"SystemRoot=" + systemRoot,
			"PATH=" + filepath.Join(systemRoot, "System32"),
		}
	}
	return []string{"PATH=/usr/bin:/bin"}
}

type boundedHookOutput struct {
	data     []byte
	tooLarge bool
}

func (output *boundedHookOutput) Write(value []byte) (int, error) {
	written := len(value)
	remaining := MaxHookOutputBytes + 1 - len(output.data)
	if remaining > 0 {
		if len(value) < remaining {
			remaining = len(value)
		}
		output.data = append(output.data, value[:remaining]...)
	}
	if len(output.data) > MaxHookOutputBytes || len(value) > remaining {
		output.tooLarge = true
	}
	return written, nil
}

func normalizePolicyText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func containsControl(value string) bool {
	for _, current := range value {
		if unicode.IsControl(current) {
			return true
		}
	}
	return false
}

// wildcardMatch implements the intentionally small name-pattern language:
// '*' matches zero or more normalized Unicode code points and every other
// character is literal. It is bounded by configuration limits and does not
// execute a regular expression.
func wildcardMatch(pattern, value string) bool {
	values := []rune(value)
	matched := make([]bool, len(values)+1)
	matched[0] = true
	for _, token := range pattern {
		next := make([]bool, len(values)+1)
		if token == '*' {
			next[0] = matched[0]
			for index := 1; index <= len(values); index++ {
				next[index] = matched[index] || next[index-1]
			}
		} else {
			for index := 1; index <= len(values); index++ {
				next[index] = matched[index-1] && values[index-1] == token
			}
		}
		matched = next
	}
	return matched[len(values)]
}
