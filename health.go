package main

import (
	"sync"
	"time"
)

const (
	minAccountCooldown = 100 * time.Millisecond
	maxAccountCooldown = 5 * time.Minute
)

// comboRuntime manages per-group mutable selector state for all combos.
// It is the replacement for routeRuntime for the combo execution path.
// Legacy routeRuntime is preserved for existing execute/stream code during transition.
type comboRuntime struct {
	mu        sync.Mutex
	persistMu sync.Mutex
	now       func() time.Time
	groups    map[string]*groupRuntime // key = candidateKey prefix "aliasKey\x00groupKey"

	accountMu       sync.Mutex
	accountCurrent  map[string]map[string]int
	accountCooldown map[string]map[string]time.Time // [poolSig][logicalKey] = coolUntil; logicalKey = account.key + "\x00" + account.id

	// Optional callback invoked after each mutation releases state lock.
	// Persists current state; nil callback is safe (no-op).
	onMutate func(map[string]map[string]map[string]*candidateHealth)
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

	// candidateKeys holds known candidate keys from groupPlan.candidates.
	// Used to filter RestoreHealth and enforce "only known candidates" invariant.
	candidateKeys map[string]struct{}
}

// candidateHealth tracks cooldown and circuit state for one candidate within one group.
type candidateHealth struct {
	CoolUntil           time.Time
	ConsecutiveFailures int
	CircuitOpen         bool
	CircuitOpenUntil    time.Time
	CircuitReopenCount  int
	HalfOpenOccupied    bool
}

func newComboRuntime(now func() time.Time) *comboRuntime {
	if now == nil {
		now = time.Now
	}
	return &comboRuntime{
		now:             now,
		groups:          make(map[string]*groupRuntime),
		accountCurrent:  make(map[string]map[string]int),
		accountCooldown: make(map[string]map[string]time.Time),
	}
}
func (r *comboRuntime) Sync(plans []comboPlan) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Rebuild group map, preserving health for unchanged signatures
	next := make(map[string]*groupRuntime, len(plans)*2)
	for _, plan := range plans {
		for _, g := range plan.groups {
			gkey := plan.aliasKey + "\x00" + g.groupKey
			sig := groupSignature(g)
			old := r.groups[gkey]
			if old != nil && old.signature == sig {
				// Preserve existing state
				next[gkey] = old
			} else {
				next[gkey] = newGroupRuntime(sig, g)
			}
		}
	}
	r.groups = next
	r.pruneAccountState(plans)
}

// pruneAccountState keeps account selector state only for pool signatures present in plans.
// Changed, removed, or new pools never inherit stale state from earlier plans.
func (r *comboRuntime) pruneAccountState(plans []comboPlan) {
	live := make(map[string]struct{})
	for _, plan := range plans {
		for _, group := range plan.groups {
			for _, candidate := range group.candidates {
				if candidate.accountPool != nil {
					live[candidate.accountPool.signature] = struct{}{}
				}
			}
		}
	}
	r.accountMu.Lock()
	defer r.accountMu.Unlock()
	for signature := range r.accountCurrent {
		if _, ok := live[signature]; !ok {
			delete(r.accountCurrent, signature)
		}
	}
	for signature := range r.accountCooldown {
		if _, ok := live[signature]; !ok {
			delete(r.accountCooldown, signature)
		}
	}
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
				if cooldown := r.accountCooldown[candidate.accountPool.signature]; cooldown != nil {
					copied := make(map[string]time.Time, len(cooldown))
					for key, until := range cooldown {
						copied[key] = until
					}
					cloned.accountCooldown[candidate.accountPool.signature] = copied
				}
			}
		}
	}
	r.accountMu.Unlock()
	return cloned
}

// SetMutateCallback configures optional callback invoked after each mutation (MarkFailure/MarkSuccess/ReleaseProbe) with updated snapshot.
// Safe to call concurrently; overwrites any prior callback.
func (r *comboRuntime) SetMutateCallback(cb func(map[string]map[string]map[string]*candidateHealth)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onMutate = cb
}

func newGroupRuntime(sig string, g groupPlan) *groupRuntime {
	candidateKeys := make(map[string]struct{}, len(g.candidates))
	for _, c := range g.candidates {
		candidateKeys[c.candidateKey] = struct{}{}
	}
	gr := &groupRuntime{
		signature:         sig,
		wrrCurrentWeights: make([]int64, len(g.candidates)),
		health:            make(map[string]*candidateHealth, len(g.candidates)),
		candidateKeys:     candidateKeys,
	}
	return gr
}

func cloneGroupRuntime(old *groupRuntime, sig string, g groupPlan) *groupRuntime {
	gr := newGroupRuntime(sig, g)
	gr.cursor = old.cursor
	copy(gr.wrrCurrentWeights, old.wrrCurrentWeights)
	for k, v := range old.health {
		if v == nil {
			continue
		}
		copy := *v
		gr.health[k] = &copy
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
	var snapshot map[string]*candidateHealth
	var onMutate func(map[string]map[string]map[string]*candidateHealth)
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	r.mu.Lock()
	gkey := runtimeGroupKey(plan.aliasKey, g.groupKey)
	gr := r.groups[gkey]
	if gr != nil {
		h := gr.health[c.candidateKey]
		if h == nil {
			h = &candidateHealth{}
			gr.health[c.candidateKey] = h
		}
		now := r.now()
		h.CoolUntil = now.Add(time.Duration(g.cooldownSec) * time.Second)
		h.ConsecutiveFailures++
		if !h.CircuitOpen && h.ConsecutiveFailures >= resilience.CircuitFailureThreshold {
			h.CircuitOpen = true
			h.CircuitOpenUntil = now.Add(circuitOpenDuration(h.CircuitReopenCount, resilience))
		}
		if h.HalfOpenOccupied {
			h.HalfOpenOccupied = false
			h.CircuitReopenCount++
			h.CircuitOpenUntil = now.Add(circuitOpenDuration(h.CircuitReopenCount, resilience))
		}
		snapshot = r.snapshotGroupHealthLocked(gkey)
		onMutate = r.onMutate
	}
	r.mu.Unlock()
	if onMutate != nil && snapshot != nil {
		onMutate(map[string]map[string]map[string]*candidateHealth{plan.aliasKey: {g.groupKey: snapshot}})
	}
}

// ReleaseProbe makes an expired-circuit probe eligible again without treating
// its result as either health success or failure.
func (r *comboRuntime) ReleaseProbe(plan comboPlan, g groupPlan, c candidatePlan) {
	var snapshot map[string]*candidateHealth
	var onMutate func(map[string]map[string]map[string]*candidateHealth)
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	r.mu.Lock()
	gkey := runtimeGroupKey(plan.aliasKey, g.groupKey)
	gr := r.groups[gkey]
	if gr != nil {
		if h := gr.health[c.candidateKey]; h != nil {
			h.HalfOpenOccupied = false
		}
		snapshot = r.snapshotGroupHealthLocked(gkey)
		onMutate = r.onMutate
	}
	r.mu.Unlock()
	if onMutate != nil && snapshot != nil {
		onMutate(map[string]map[string]map[string]*candidateHealth{plan.aliasKey: {g.groupKey: snapshot}})
	}
}

// MarkSuccess resets health state after a successful call.
func (r *comboRuntime) MarkSuccess(plan comboPlan, g groupPlan, c candidatePlan) {
	var snapshot map[string]*candidateHealth
	var onMutate func(map[string]map[string]map[string]*candidateHealth)
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	r.mu.Lock()
	gkey := runtimeGroupKey(plan.aliasKey, g.groupKey)
	gr := r.groups[gkey]
	if gr != nil {
		h := gr.health[c.candidateKey]
		if h != nil {
			h.ConsecutiveFailures = 0
			h.CircuitOpen = false
			h.CircuitOpenUntil = time.Time{}
			h.CircuitReopenCount = 0
			h.HalfOpenOccupied = false
			h.CoolUntil = time.Time{}
			snapshot = r.snapshotGroupHealthLocked(gkey)
			onMutate = r.onMutate
		}
	}
	r.mu.Unlock()
	if onMutate != nil && snapshot != nil {
		onMutate(map[string]map[string]map[string]*candidateHealth{plan.aliasKey: {g.groupKey: snapshot}})
	}
}
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

// snapshotGroupHealthLocked copies a single group's health state while caller holds r.mu.
// Must only be called while r.mu is held; caller responsible for lock management.
func (r *comboRuntime) snapshotGroupHealthLocked(gkey string) map[string]*candidateHealth {
	gr := r.groups[gkey]
	if gr == nil || len(gr.health) == 0 {
		return nil
	}
	result := make(map[string]*candidateHealth)
	for ckey, health := range gr.health {
		if health == nil {
			continue
		}
		result[ckey] = &candidateHealth{
			CoolUntil:           health.CoolUntil,
			ConsecutiveFailures: health.ConsecutiveFailures,
			CircuitOpen:         health.CircuitOpen,
			CircuitOpenUntil:    health.CircuitOpenUntil,
			CircuitReopenCount:  health.CircuitReopenCount,
			HalfOpenOccupied:    health.HalfOpenOccupied,
		}
	}
	return result
}

// SnapshotHealth returns all current health state for persistence.
func (r *comboRuntime) SnapshotHealth() map[string]map[string]*candidateHealth {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make(map[string]map[string]*candidateHealth)
	for gkey, gruntime := range r.groups {
		if gruntime == nil || len(gruntime.health) == 0 {
			continue
		}
		result[gkey] = make(map[string]*candidateHealth)
		for ckey, health := range gruntime.health {
			if health == nil {
				continue
			}
			result[gkey][ckey] = &candidateHealth{
				CoolUntil:           health.CoolUntil,
				ConsecutiveFailures: health.ConsecutiveFailures,
				CircuitOpen:         health.CircuitOpen,
				CircuitOpenUntil:    health.CircuitOpenUntil,
				CircuitReopenCount:  health.CircuitReopenCount,
				HalfOpenOccupied:    health.HalfOpenOccupied,
			}
		}
	}
	return result
}

// RestoreHealth replaces current health state with persisted snapshot.
// It restores only known current candidates and copies supplied health values.
func (r *comboRuntime) RestoreHealth(snapshot map[string]map[string]*candidateHealth) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, gr := range r.groups {
		if gr != nil {
			gr.health = make(map[string]*candidateHealth)
		}
	}
	for gkey, candMap := range snapshot {
		gr := r.groups[gkey]
		if gr == nil {
			continue
		}
		for ckey, health := range candMap {
			if health == nil {
				continue
			}
			if _, isKnown := gr.candidateKeys[ckey]; !isKnown {
				continue
			}
			healthCopy := *health
			healthCopy.HalfOpenOccupied = false
			gr.health[ckey] = &healthCopy
		}
	}
}

// ResetHealth clears all health state (utility for testing or soft resets).
func (r *comboRuntime) ResetHealth() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, gr := range r.groups {
		if gr != nil {
			gr.health = make(map[string]*candidateHealth)
		}
	}
}

// ResetComboGroupHealth clears health state for a specific combo+group in runtime only.
func (r *comboRuntime) ResetComboGroupHealth(comboAliasKey, groupKey string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if gr, ok := r.groups[comboAliasKey+"\x00"+groupKey]; ok && gr != nil {
		gr.health = make(map[string]*candidateHealth)
	}
}

// ResetComboGroupHealthAndPersist clears one runtime group and its persisted state atomically against mutations.
func (r *comboRuntime) ResetComboGroupHealthAndPersist(comboAliasKey, groupKey string, persist func() error) error {
	if r == nil {
		return persist()
	}
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	if err := persist(); err != nil {
		return err
	}
	r.mu.Lock()
	if gr := r.groups[runtimeGroupKey(comboAliasKey, groupKey)]; gr != nil {
		gr.health = make(map[string]*candidateHealth)
	}
	r.mu.Unlock()
	return nil
}
