package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

// ComputerToolsConfig is an independent, opt-in grant. Browser, shell and node
// connectivity alone never grant access to a native desktop. Targets select
// operator-owned aliases, never caller-authored display/PID/window coordinates.
type ComputerToolsConfig struct {
	Enabled bool                            `json:"enabled"           yaml:"-"`
	Targets map[string]ComputerTargetConfig `json:"targets,omitempty" yaml:"-"`
}

type ComputerTargetConfig struct {
	NodeTarget         string            `json:"node_target"`
	Profile            string            `json:"profile"`
	Revision           string            `json:"revision"`
	Actors             []string          `json:"actors"`
	Agents             []string          `json:"agents"`
	Runtimes           []runtimecap.Kind `json:"runtimes"`
	LeaseSeconds       int               `json:"lease_seconds,omitempty"`
	ObservationSeconds int               `json:"observation_seconds,omitempty"`
}

func (target ComputerTargetConfig) LeaseTTL() time.Duration {
	if target.LeaseSeconds == 0 {
		return 300 * time.Second
	}
	return time.Duration(target.LeaseSeconds) * time.Second
}

func (target ComputerTargetConfig) ObservationTTL() time.Duration {
	if target.ObservationSeconds == 0 {
		return 15 * time.Second
	}
	return time.Duration(target.ObservationSeconds) * time.Second
}

func (target ComputerTargetConfig) Admits(principal runtimecap.Principal) bool {
	return principal.Validate() == nil && slices.Contains(target.Actors, principal.ActorID) &&
		slices.Contains(target.Agents, principal.AgentID) && slices.Contains(target.Runtimes, principal.Runtime)
}

func (cfg *Config) ValidateComputerConfig() error {
	if cfg == nil {
		return errors.New("computer config requires a root config")
	}
	policy := cfg.Tools.Computer
	if len(policy.Targets) > 32 || policy.Enabled && (!cfg.Nodes.Enabled || len(policy.Targets) == 0) {
		return errors.New("computer access requires bounded explicit node targets")
	}
	for alias, target := range policy.Targets {
		execution, found := cfg.Execution.Targets[target.NodeTarget]
		digest, digestErr := hex.DecodeString(target.Revision)
		if !validExecutionTargetName(alias) || !validExecutionTargetName(target.NodeTarget) ||
			!found || execution.Type != "node" || !validNodeReference(execution.Node) ||
			!validExecutionTargetName(target.Profile) || digestErr != nil || len(digest) != sha256.Size ||
			target.LeaseSeconds < 0 || target.LeaseSeconds > 1800 ||
			target.ObservationSeconds < 0 || target.ObservationSeconds > 60 ||
			target.ObservationTTL() > target.LeaseTTL() ||
			len(target.Actors) == 0 || len(target.Actors) > 64 || len(target.Agents) == 0 || len(target.Agents) > 64 ||
			len(target.Runtimes) == 0 || len(target.Runtimes) > 2 {
			return errors.New("computer target requires exact revision, owners, runtimes and bounded lifetimes")
		}
		for _, actor := range target.Actors {
			if (runtimecap.Principal{
				Runtime: runtimecap.KindGateway, ActorID: actor,
				AgentID: "shape", SessionID: "shape", ExecutionID: "shape",
			}).Validate() != nil {
				return errors.New("computer target actor is invalid")
			}
		}
		for _, agent := range target.Agents {
			if !validExecutionTargetName(agent) {
				return errors.New("computer target agent is invalid")
			}
		}
		for _, kind := range target.Runtimes {
			if !kind.Valid() {
				return errors.New("computer target runtime is invalid")
			}
		}
	}
	return nil
}

// PolicyRevision includes the transport definitions selected by this grant.
// Reassigning an execution alias must retire existing tool authority too.
func (policy ComputerToolsConfig) PolicyRevision(execution map[string]ExecutionTarget) string {
	selected := make(map[string]ExecutionTarget, len(policy.Targets))
	for _, target := range policy.Targets {
		selected[target.NodeTarget] = execution[target.NodeTarget]
	}
	encoded, _ := json.Marshal(struct {
		Policy    ComputerToolsConfig
		Execution map[string]ExecutionTarget
	}{policy, selected})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
