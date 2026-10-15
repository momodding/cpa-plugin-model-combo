package main

import (
	"math"
	"time"
)

// attemptLedger tracks per-request attempt state. Never shared between requests.
type attemptLedger struct {
	attempted   map[string]struct{} // candidate keys attempted this request
	count       int
	maxAttempts int
	deadline    time.Time
	lastErr     error
	committed   bool // stream has emitted first payload; no further fallback allowed
}

func newAttemptLedger(maxAttempts int, deadlineMs int) *attemptLedger {
	dl := time.Time{}
	if deadlineMs > 0 {
		dl = time.Now().Add(time.Duration(deadlineMs) * time.Millisecond)
	}
	return &attemptLedger{
		attempted:   make(map[string]struct{}, maxAttempts),
		maxAttempts: maxAttempts,
		deadline:    dl,
	}
}

func (l *attemptLedger) canAttempt() bool {
	if l.count >= l.maxAttempts {
		return false
	}
	if !l.deadline.IsZero() && time.Now().After(l.deadline) {
		return false
	}
	return true
}

func (l *attemptLedger) markAttempted(key string) {
	l.markCandidateExhausted(key)
	l.recordAttempt()
}

func (l *attemptLedger) markCandidateExhausted(key string) {
	l.attempted[key] = struct{}{}
}

func (l *attemptLedger) recordAttempt() {
	l.count++
}

func (l *attemptLedger) alreadyAttempted(key string) bool {
	_, ok := l.attempted[key]
	return ok
}

func (l *attemptLedger) remainingBudget() time.Duration {
	if l.deadline.IsZero() {
		return time.Duration(math.MaxInt64)
	}
	rem := time.Until(l.deadline)
	if rem < 0 {
		return 0
	}
	return rem
}

// eligibleEntry is one candidate that passed health/attempt filtering for this pick.
type eligibleEntry struct {
	idx int // position in groupPlan.candidates
	c   candidatePlan
}

// comboSelection is the result of one group-level selection.
type comboSelection struct {
	plan         comboPlan
	group        groupPlan
	candidate    candidatePlan
	candidateIdx int
	allCooling   bool
	retryAfter   time.Duration // set when allCooling
}

// selectFromGroup picks one eligible candidate from g using g's strategy.
// Returns (selection, true) if a candidate was found; allCooling if health-gated;
// empty if exhausted normally (all already attempted or no candidates).
// Must be called without holding the runtime lock; acquires it internally.
func (r *comboRuntime) selectFromGroup(plan comboPlan, g groupPlan, ledger *attemptLedger, requirements ...map[modelCapability]struct{}) comboSelection {
	r.mu.Lock()
	defer r.mu.Unlock()

	gkey := runtimeGroupKey(plan.aliasKey, g.groupKey)
	gr := r.groups[gkey]
	if gr == nil {
		return comboSelection{}
	}

	now := r.now()
	var required map[modelCapability]struct{}
	if len(requirements) > 0 {
		required = requirements[0]
	}

	eligible := make([]eligibleEntry, 0, len(g.candidates))
	var worstRetryAfter time.Duration
	anyHealthBlocked := false

	for idx, c := range g.candidates {
		if !candidateSupports(c, plan.capabilityPolicy, required) {
			continue
		}
		if ledger.alreadyAttempted(c.candidateKey) {
			continue
		}
		h := gr.health[c.candidateKey]
		if h == nil {
			eligible = append(eligible, eligibleEntry{idx, c})
			continue
		}
		// Cooldown gate.
		if now.Before(h.coolUntil) {
			ra := h.coolUntil.Sub(now)
			if ra > worstRetryAfter {
				worstRetryAfter = ra
			}
			anyHealthBlocked = true
			continue
		}
		// Circuit gate.
		if h.circuitOpen {
			if now.Before(h.circuitOpenUntil) {
				ra := h.circuitOpenUntil.Sub(now)
				if ra > worstRetryAfter {
					worstRetryAfter = ra
				}
				anyHealthBlocked = true
				continue
			}
			// Duration elapsed → allow exactly one half-open probe.
			if h.halfOpenOccupied {
				anyHealthBlocked = true
				continue
			}
			// Claim half-open probe; released by MarkSuccess/MarkFailure.
			h.halfOpenOccupied = true
		}
		eligible = append(eligible, eligibleEntry{idx, c})
	}

	if len(eligible) == 0 {
		if anyHealthBlocked {
			return comboSelection{allCooling: true, retryAfter: worstRetryAfter}
		}
		return comboSelection{} // exhausted
	}

	switch g.strategy {
	case routeStrategyPriority:
		e := eligible[0]
		return comboSelection{plan: plan, group: g, candidate: e.c, candidateIdx: e.idx}

	case routeStrategyRoundRobin:
		pick := int(gr.cursor) % len(eligible)
		gr.cursor++
		e := eligible[pick]
		return comboSelection{plan: plan, group: g, candidate: e.c, candidateIdx: e.idx}

	case routeStrategyWeightedRoundRobin:
		return r.selectWRR(gr, g, eligible, plan)

	default:
		// Unknown strategy: fall back to priority (validated at config decode time).
		e := eligible[0]
		return comboSelection{plan: plan, group: g, candidate: e.c, candidateIdx: e.idx}
	}
}

// selectWRR implements smooth weighted round-robin over eligible candidates only.
// Algorithm: current[i] += weight[i]; pick max; subtract total from winner.
// Only eligible positions participate; skipped candidates do not accumulate drift.
func (r *comboRuntime) selectWRR(gr *groupRuntime, g groupPlan, eligible []eligibleEntry, plan comboPlan) comboSelection {
	// Accumulate.
	for _, e := range eligible {
		w := int64(e.c.weight)
		if w <= 0 {
			w = 1
		}
		gr.wrrCurrentWeights[e.idx] += w
	}

	// Pick max; tie breaks by YAML order (lower index wins).
	best := eligible[0]
	bestW := gr.wrrCurrentWeights[eligible[0].idx]
	for _, e := range eligible[1:] {
		w := gr.wrrCurrentWeights[e.idx]
		if w > bestW || (w == bestW && e.idx < best.idx) {
			best = e
			bestW = w
		}
	}

	// Compute eligible total weight.
	var total int64
	for _, e := range eligible {
		w := int64(e.c.weight)
		if w <= 0 {
			w = 1
		}
		total += w
	}

	gr.wrrCurrentWeights[best.idx] -= total
	return comboSelection{plan: plan, group: g, candidate: best.c, candidateIdx: best.idx}
}
