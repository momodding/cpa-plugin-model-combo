package main

import (
	"sync"
	"time"
)

// comboRuntime manages per-group mutable selector state for all combos.
// It is the replacement for routeRuntime for the combo execution path.
// Legacy routeRuntime is preserved for existing execute/stream code during transition.
type comboRuntime struct {
	mu     sync.Mutex
	now    func() time.Time
	groups map[string]*groupRuntime // key = candidateKey prefix "aliasKey\x00groupKey"

	accountMu      sync.Mutex
	accountCurrent map[string]map[string]int
}

// groupRuntime holds per-group mutable state: cursor (RR), smooth WRR current weights,
// and per-candidate cooldown + circuit state.
type groupRuntime struct {
	signature string
	cursor    int64

	// Smooth weighted RR current weights, indexed by candidate position.
	// Reset when group signature changes.
	wrrCurrentWeights []int64

	// Health state per candidate key.
	health map[string]*candidateHealth
}

// candidateHealth tracks cooldown and circuit state for one candidate within one group.
type candidateHealth struct {
	// Cooldown: candidate is skipped until this time expires.
	coolUntil time.Time

	// Circuit breaker.
	consecutiveFailures int
	circuitOpen         bool
	circuitOpenUntil    time.Time
	circuitReopenCount  int
	halfOpenOccupied    bool
}

func newComboRuntime(now func() time.Time) *comboRuntime {
	if now == nil {
		now = time.Now
	}
	return &comboRuntime{now: now, groups: make(map[string]*groupRuntime), accountCurrent: make(map[string]map[string]int)}
}

// Sync installs fresh group state for new plans; preserves compatible existing state.
func (r *comboRuntime) Sync(plans []comboPlan) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]*groupRuntime, len(plans)*2)
	for _, plan := range plans {
		for _, g := range plan.groups {
			gkey := plan.aliasKey + "\x00" + g.groupKey
			sig := groupSignature(g)
			old := r.groups[gkey]
			if old != nil && old.signature == sig {
				next[gkey] = old
			} else {
				next[gkey] = newGroupRuntime(sig, g)
			}
		}
	}
	r.groups = next
}

// Clone creates a new runtime preserving state for groups whose signature is unchanged.
func (r *comboRuntime) Clone(plans []comboPlan, now func() time.Time) *comboRuntime {
	cloned := newComboRuntime(now)
	if r == nil {
		cloned.Sync(plans)
		return cloned
	}
	r.mu.Lock()
	next := make(map[string]*groupRuntime, len(plans)*2)
	for _, plan := range plans {
		for _, g := range plan.groups {
			gkey := plan.aliasKey + "\x00" + g.groupKey
			sig := groupSignature(g)
			old := r.groups[gkey]
			if old != nil && old.signature == sig {
				// Deep-copy preserved state.
				next[gkey] = cloneGroupRuntime(old, sig, g)
			} else {
				next[gkey] = newGroupRuntime(sig, g)
			}
		}
	}
	r.mu.Unlock()
	cloned.groups = next
	r.accountMu.Lock()
	for _, plan := range plans {
		for _, group := range plan.groups {
			for _, candidate := range group.candidates {
				if candidate.accountPool == nil {
					continue
				}
				if current := r.accountCurrent[candidate.accountPool.signature]; current != nil {
					copied := make(map[string]int, len(current))
					for key, weight := range current {
						copied[key] = weight
					}
					cloned.accountCurrent[candidate.accountPool.signature] = copied
				}
			}
		}
	}
	r.accountMu.Unlock()
	return cloned
}

func newGroupRuntime(sig string, g groupPlan) *groupRuntime {
	gr := &groupRuntime{
		signature:         sig,
		wrrCurrentWeights: make([]int64, len(g.candidates)),
		health:            make(map[string]*candidateHealth, len(g.candidates)),
	}
	for _, c := range g.candidates {
		gr.health[c.candidateKey] = &candidateHealth{}
	}
	return gr
}

func cloneGroupRuntime(old *groupRuntime, sig string, g groupPlan) *groupRuntime {
	gr := &groupRuntime{
		signature:         sig,
		cursor:            old.cursor,
		wrrCurrentWeights: make([]int64, len(g.candidates)),
		health:            make(map[string]*candidateHealth, len(g.candidates)),
	}
	copy(gr.wrrCurrentWeights, old.wrrCurrentWeights)
	for _, c := range g.candidates {
		if h, ok := old.health[c.candidateKey]; ok {
			copied := *h
			gr.health[c.candidateKey] = &copied
		} else {
			gr.health[c.candidateKey] = &candidateHealth{}
		}
	}
	return gr
}

// groupKey returns the map key for a combo+group pair.
func runtimeGroupKey(comboAliasKey, groupKey string) string {
	return comboAliasKey + "\x00" + groupKey
}

// MarkFailure records a health failure for a candidate; updates cooldown and circuit.
// Called under caller's logic; acquires runtime lock internally.
func (r *comboRuntime) MarkFailure(plan comboPlan, g groupPlan, c candidatePlan, resilience resilienceConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gkey := runtimeGroupKey(plan.aliasKey, g.groupKey)
	gr := r.groups[gkey]
	if gr == nil {
		return
	}
	h := gr.health[c.candidateKey]
	if h == nil {
		h = &candidateHealth{}
		gr.health[c.candidateKey] = h
	}
	now := r.now()

	// Cooldown: restart on every health failure.
	h.coolUntil = now.Add(time.Duration(g.cooldownSec) * time.Second)

	// Circuit: count consecutive failures.
	h.consecutiveFailures++
	if !h.circuitOpen && h.consecutiveFailures >= resilience.CircuitFailureThreshold {
		h.circuitOpen = true
		openDur := circuitOpenDuration(h.circuitReopenCount, resilience)
		h.circuitOpenUntil = now.Add(openDur)
	}
	// Release half-open probe on failure → reopen.
	if h.halfOpenOccupied {
		h.halfOpenOccupied = false
		h.circuitReopenCount++
		openDur := circuitOpenDuration(h.circuitReopenCount, resilience)
		h.circuitOpenUntil = now.Add(openDur)
	}
}

// ReleaseProbe makes an expired-circuit probe eligible again without treating
// its result as either health success or failure.
func (r *comboRuntime) ReleaseProbe(plan comboPlan, g groupPlan, c candidatePlan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gr := r.groups[runtimeGroupKey(plan.aliasKey, g.groupKey)]
	if gr == nil {
		return
	}
	if h := gr.health[c.candidateKey]; h != nil {
		h.halfOpenOccupied = false
	}
}

// MarkSuccess resets health state after a successful call.
func (r *comboRuntime) MarkSuccess(plan comboPlan, g groupPlan, c candidatePlan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gkey := runtimeGroupKey(plan.aliasKey, g.groupKey)
	gr := r.groups[gkey]
	if gr == nil {
		return
	}
	h := gr.health[c.candidateKey]
	if h == nil {
		return
	}
	h.consecutiveFailures = 0
	h.circuitOpen = false
	h.circuitOpenUntil = time.Time{}
	h.circuitReopenCount = 0
	h.halfOpenOccupied = false
	h.coolUntil = time.Time{}
}

// circuitOpenDuration computes doubling open duration capped at max.
func circuitOpenDuration(reopenCount int, resilience resilienceConfig) time.Duration {
	base := time.Duration(resilience.CircuitOpenSeconds) * time.Second
	maxDur := time.Duration(resilience.CircuitMaxOpenSeconds) * time.Second
	dur := base
	for range reopenCount {
		dur *= 2
		if dur >= maxDur {
			return maxDur
		}
	}
	if dur > maxDur {
		return maxDur
	}
	return dur
}
