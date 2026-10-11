// Package computer defines provider-neutral contracts for desktop capability
// hosts. These types do not grant authority, register tools, launch drivers, or
// control an OS desktop. Trusted runtime and host policy must admit each request.
package computer

import (
	"context"
	"errors"
	"math"
	"regexp"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

var (
	ErrInvalid        = errors.New("invalid computer contract")
	ErrDenied         = errors.New("computer authority denied")
	ErrStale          = errors.New("stale computer control generation")
	ErrExpired        = errors.New("computer lease or observation expired")
	ErrConflict       = errors.New("invalid computer control transition")
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
)

type (
	Placement       string
	OperatingSystem string
	DesktopMode     string
	Isolation       string
)

const (
	PlacementGateway   Placement       = "gateway"
	PlacementNode      Placement       = "node"
	OSLinux            OperatingSystem = "linux"
	OSMacOS            OperatingSystem = "macos"
	ModeAttached       DesktopMode     = "attached"
	ModeManaged        DesktopMode     = "managed"
	IsolationNone      Isolation       = "none"
	IsolationContainer Isolation       = "container"
	IsolationVM        Isolation       = "vm"
)

// Scope identifies a host-selected desktop, not an ambient display address.
// WindowGeneration prevents a reused window ID from identifying a replacement.
type Scope struct {
	DesktopID        string `json:"desktop_id"`
	DisplayID        string `json:"display_id"`
	WindowID         string `json:"window_id,omitempty"`
	WindowGeneration uint64 `json:"window_generation,omitempty"`
}

func (scope Scope) Validate() error {
	if !validIdentifier(scope.DesktopID) || !validIdentifier(scope.DisplayID) {
		return ErrInvalid
	}
	if scope.WindowID == "" {
		if scope.WindowGeneration != 0 {
			return ErrInvalid
		}
	} else if !validIdentifier(scope.WindowID) || scope.WindowGeneration == 0 {
		return ErrInvalid
	}
	return nil
}

// Target keeps hosting, ownership, OS, and isolation independent. Isolation is
// an operator/host assertion to qualify, not a sandbox created by this type.
type Target struct {
	Alias     string          `json:"alias"`
	Placement Placement       `json:"placement"`
	Node      string          `json:"node,omitempty"`
	OS        OperatingSystem `json:"os"`
	Mode      DesktopMode     `json:"mode"`
	Isolation Isolation       `json:"isolation"`
	Backend   string          `json:"backend"`
	Scope     Scope           `json:"scope"`
}

func (target Target) Validate() error {
	if !validIdentifier(target.Alias) || !validIdentifier(target.Backend) || target.Scope.Validate() != nil {
		return ErrInvalid
	}
	if target.Placement != PlacementGateway && target.Placement != PlacementNode ||
		target.Placement == PlacementNode && !validIdentifier(target.Node) ||
		target.Placement == PlacementGateway && target.Node != "" {
		return ErrInvalid
	}
	if target.OS != OSLinux && target.OS != OSMacOS ||
		target.Mode != ModeAttached && target.Mode != ModeManaged ||
		target.Isolation != IsolationNone && target.Isolation != IsolationContainer && target.Isolation != IsolationVM {
		return ErrInvalid
	}
	return nil
}

// Binding is trusted per-session authority. It is not a model-authored target
// selector. Replacements of a worker, policy, scope, or owner invalidate evidence.
type Binding struct {
	Principal      runtimecap.Principal `json:"principal"`
	SessionID      string               `json:"session_id"`
	Target         Target               `json:"target"`
	WorkerID       string               `json:"worker_id"`
	PolicyRevision string               `json:"policy_revision"`
	Generation     uint64               `json:"generation"`
}

func (binding Binding) Validate() error {
	if binding.Principal.Validate() != nil || binding.Target.Validate() != nil || binding.Generation == 0 ||
		!validIdentifier(
			binding.SessionID,
		) || !validIdentifier(binding.WorkerID) || !validIdentifier(binding.PolicyRevision) {
		return ErrInvalid
	}
	return nil
}

type (
	ControlState string
	ControlEvent string
)

const (
	ControlAgent         ControlState = "agent"
	ControlHuman         ControlState = "human"
	ControlResumePending ControlState = "resume_pending"
	ControlReleased      ControlState = "released"
	ControlLost          ControlState = "lost"
	EventHandoff         ControlEvent = "handoff"
	EventRequestResume   ControlEvent = "request_resume"
	EventResume          ControlEvent = "resume"
	EventRelease         ControlEvent = "release"
	EventLose            ControlEvent = "lose"
)

func (state ControlState) Valid() bool {
	return state == ControlAgent || state == ControlHuman || state == ControlResumePending || state.Terminal()
}

func (state ControlState) Terminal() bool {
	return state == ControlReleased || state == ControlLost
}

// Session tracks automation ownership only. Released/lost does not say whether
// a user's application is open or a managed environment was destroyed.
type Session struct {
	Binding   Binding      `json:"binding"`
	Control   ControlState `json:"control"`
	ExpiresAt time.Time    `json:"expires_at"`
}

func (session Session) Validate() error {
	if session.Binding.Validate() != nil || !session.Control.Valid() || session.ExpiresAt.IsZero() {
		return ErrInvalid
	}
	return nil
}

func (session Session) AgentAuthority(now time.Time) error {
	if session.Validate() != nil || now.IsZero() {
		return ErrInvalid
	}
	if session.Control != ControlAgent {
		return ErrDenied
	}
	if !now.Before(session.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

// Transition is a side-effect-free state calculation. Hosts must serialize and
// persist the transition, enforce expiry, stop input before handoff, and enforce
// real revocation.
// It interprets typed events, never natural-language finish phrases.
func (session Session) Transition(event ControlEvent, expectedGeneration uint64) (Session, error) {
	if session.Validate() != nil {
		return Session{}, ErrInvalid
	}
	if session.Binding.Generation != expectedGeneration {
		return Session{}, ErrStale
	}
	var next ControlState
	switch {
	case event == EventRelease && !session.Control.Terminal():
		next = ControlReleased
	case event == EventLose && !session.Control.Terminal():
		next = ControlLost
	case event == EventRelease && session.Control.Terminal():
		return session, nil
	case event == EventHandoff && session.Control == ControlAgent:
		next = ControlHuman
	case event == EventRequestResume && session.Control == ControlHuman:
		next = ControlResumePending
	case event == EventResume && session.Control == ControlResumePending:
		next = ControlAgent
	default:
		return Session{}, ErrConflict
	}
	if session.Binding.Generation == math.MaxUint64 {
		return Session{}, ErrStale
	}
	session.Control = next
	session.Binding.Generation++
	return session, nil
}

type OpenRequest struct {
	Principal runtimecap.Principal
	Target    Target
}

func (request OpenRequest) Validate() error {
	if request.Principal.Validate() != nil || request.Target.Validate() != nil {
		return ErrInvalid
	}
	return nil
}

// Host is the initial observation/control contract for an injected companion or
// local host. Targets returns only admitted aliases without launching/capturing.
// Every operation must enforce local policy and stored ownership/control identity;
// validation alone is not authorization.
// Release revokes automation, never implicitly closes user-owned applications.
// Input and managed-environment destruction are intentionally separate later APIs.
type Host interface {
	Targets(context.Context, runtimecap.Principal) ([]Target, error)
	Open(context.Context, OpenRequest) (Session, error)
	Status(context.Context, runtimecap.Principal, string) (Session, error)
	Observe(context.Context, Session) (Observation, error)
	Handoff(context.Context, Binding) (Session, error)
	RequestResume(context.Context, Binding) (Session, error)
	Resume(context.Context, Binding) (Session, error)
	Release(context.Context, Binding) (Session, error)
}

func validIdentifier(value string) bool {
	return identifierPattern.MatchString(value)
}
