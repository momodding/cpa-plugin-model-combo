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
		if now.Before(h.CoolUntil) {
			ra := h.CoolUntil.Sub(now)
			if ra > worstRetryAfter {
				worstRetryAfter = ra
			}
			anyHealthBlocked = true
			continue
		}
		// Circuit gate.
		if h.CircuitOpen {
			if now.Before(h.CircuitOpenUntil) {
				ra := h.CircuitOpenUntil.Sub(now)
				if ra > worstRetryAfter {
					worstRetryAfter = ra
				}
				anyHealthBlocked = true
				continue
			}
			// Duration elapsed → allow exactly one half-open probe.
			if h.HalfOpenOccupied {
				anyHealthBlocked = true
				continue
			}
		}
		eligible = append(eligible, eligibleEntry{idx, c})
	}

	if len(eligible) == 0 {
		if anyHealthBlocked {
			return comboSelection{allCooling: true, retryAfter: worstRetryAfter}
		}
		return comboSelection{} // exhausted
	}

	var selected eligibleEntry
	switch g.strategy {
	case routeStrategyPriority:
		selected = eligible[0]

	case routeStrategyRoundRobin:
		pick := int(gr.cursor) % len(eligible)
		gr.cursor++
		selected = eligible[pick]

	case routeStrategyWeightedRoundRobin:
		selected = selectWRR(gr, eligible)

	default:
		// Unknown strategy: fall back to priority (validated at config decode time).
		selected = eligible[0]
	}

	// Reserve a half-open probe only after strategy selects its candidate. Reserving
	// candidates while building eligible would strand every unselected half-open peer.
	if h := gr.health[selected.c.candidateKey]; h != nil && h.CircuitOpen && !now.Before(h.CircuitOpenUntil) {
		h.HalfOpenOccupied = true
	}
	return comboSelection{plan: plan, group: g, candidate: selected.c, candidateIdx: selected.idx}
}

// selectWRR implements smooth weighted round-robin over eligible candidates only.
// Algorithm: current[i] += weight[i]; pick max; subtract total from winner.
// Only eligible positions participate; skipped candidates do not accumulate drift.
func selectWRR(gr *groupRuntime, eligible []eligibleEntry) eligibleEntry {
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
	return best
}
