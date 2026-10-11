// MintClaw - Ultra-lightweight personal AI agent

package agent

type runtimeSessionClaim struct {
	runtime     *turnRuntime
	scope       runtimeSessionScope
	routeScope  runtimeRouteScope
	routeTarget *inboundDispatchTarget
	placeholder *turnState
}

func (r *turnRuntime) claimRuntimeRouteSession(
	target *inboundDispatchTarget,
	turnID string,
) (*runtimeSessionClaim, *inboundDispatchTarget, bool) {
	r.routeAdmissionMu.Lock()
	defer r.routeAdmissionMu.Unlock()
	return r.claimRuntimeRouteSessionLocked(target, turnID)
}

func (r *turnRuntime) claimRuntimeRouteSessionLocked(
	target *inboundDispatchTarget,
	turnID string,
) (*runtimeSessionClaim, *inboundDispatchTarget, bool) {
	routeScope := target.runtimeRouteScope()
	if routeScope.workspace == "" || routeScope.claimKey == "" {
		return nil, target, false
	}
	if existing, loaded := r.activeRouteSessions.Load(routeScope); loaded {
		owner, ok := existing.(*runtimeSessionClaim)
		if !ok || owner.routeTarget == nil {
			r.activeRouteSessions.CompareAndDelete(routeScope, existing)
			return nil, target, false
		}
		return nil, owner.routeTarget, false
	}
	claim, claimed := r.claimRuntimeSession(target.runtimeSessionScope(), turnID)
	if !claimed {
		return nil, target, false
	}
	claim.routeScope = routeScope
	claim.routeTarget = target
	// Publish the fully initialized claim, retaining the original pinned target
	// while giving every owner a distinct retirement identity.
	r.activeRouteSessions.Store(routeScope, claim)
	return claim, target, true
}

func (r *turnRuntime) claimRuntimeSession(scope runtimeSessionScope, turnID string) (*runtimeSessionClaim, bool) {
	if !scope.complete() {
		return nil, false
	}
	placeholder := &turnState{
		turnID:     turnID,
		workspace:  scope.workspace,
		sessionKey: scope.sessionKey,
		phase:      TurnPhaseSetup,
	}
	if _, loaded := r.activeTurnStates.LoadOrStore(scope, placeholder); loaded {
		return nil, false
	}
	return &runtimeSessionClaim{
		runtime:     r,
		scope:       scope,
		placeholder: placeholder,
	}, true
}

func (claim *runtimeSessionClaim) releaseIfOwned() {
	if claim == nil || claim.placeholder == nil || claim.runtime == nil {
		return
	}
	claim.runtime.routeAdmissionMu.Lock()
	defer claim.runtime.routeAdmissionMu.Unlock()
	claim.releaseIfOwnedLocked()
}

func (claim *runtimeSessionClaim) releaseIfOwnedLocked() {
	claim.releaseSessionIfOwned()
	if claim.routeTarget != nil {
		claim.runtime.activeRouteSessions.CompareAndDelete(claim.routeScope, claim)
	}
}

func (claim *runtimeSessionClaim) releaseSessionIfOwned() {
	if claim == nil || claim.placeholder == nil || claim.runtime == nil {
		return
	}
	if actual, ok := claim.runtime.activeTurnStates.Load(claim.scope); ok && actual == claim.placeholder {
		claim.runtime.activeTurnStates.Delete(claim.scope)
	}
}
