// Package scope defines the coding execution authority selected before a
// task, worker, repository, or model runtime is constructed.
package scope

// Kind describes the operator-owned filesystem scope behind a remote coding
// alias. It is intentionally independent from project.ProjectKind: a machine
// scope may start inside a Git checkout without becoming an isolated-project
// authority.
type Kind string

const (
	KindGitProject Kind = "git_project"
	KindMachine    Kind = "machine"
)

func (kind Kind) Valid() bool {
	return kind == KindGitProject || kind == KindMachine
}

// Profile is the immutable authority and isolation profile for one coding
// task. Profiles are selected through operator-owned grants, never inferred
// from a command failure or from model-authored filesystem paths.
type Profile string

const (
	ProfileInvestigate     Profile = "investigate"
	ProfileMutate          Profile = "mutate"
	ProfileProjectYolo     Profile = "project-yolo"
	ProfileMachineYolo     Profile = "machine-yolo"
	ProfileMachineYoloRoot Profile = "machine-yolo-root"
)

func (profile Profile) Valid() bool {
	switch profile {
	case ProfileInvestigate,
		ProfileMutate,
		ProfileProjectYolo,
		ProfileMachineYolo,
		ProfileMachineYoloRoot:
		return true
	default:
		return false
	}
}

// AdmittedInV2 reports whether the current durable task schema and private
// worker protocol may carry this profile. Later profiles are defined here so
// the authority matrix stays centralized, but each requires an explicit
// protocol/schema admission before it can cross a runtime boundary.
func (profile Profile) AdmittedInV2() bool {
	return profile == ProfileInvestigate || profile == ProfileMutate
}

// ReadOnly reports whether the coding runtime must omit command and mutation
// tools and use the configured source root directly.
func (profile Profile) ReadOnly() bool {
	return profile == ProfileInvestigate
}

// UsesIsolatedWorktree reports whether the task requires P7.3 ownership and
// handoff around a linked Git worktree.
func (profile Profile) UsesIsolatedWorktree() bool {
	return profile == ProfileMutate || profile == ProfileProjectYolo
}

// DirectWritable reports whether the configured directory is itself the
// writable execution root. This authority does not claim filesystem rollback.
func (profile Profile) DirectWritable() bool {
	return profile == ProfileMachineYolo || profile == ProfileMachineYoloRoot
}

// Privileged reports whether a separately configured root execution backend
// must be present. It does not mean the coding worker process runs as root.
func (profile Profile) Privileged() bool {
	return profile == ProfileMachineYoloRoot
}

// AllowedFor is the closed scope/profile validation matrix.
func (profile Profile) AllowedFor(kind Kind) bool {
	if !kind.Valid() || !profile.Valid() {
		return false
	}
	switch kind {
	case KindGitProject:
		return profile.ReadOnly() || profile.UsesIsolatedWorktree()
	case KindMachine:
		return profile.DirectWritable()
	default:
		return false
	}
}
