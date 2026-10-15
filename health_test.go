package main

import (
	"testing"
	"time"
)

func TestComboRuntimeClonePreservesOnlyUnchangedAccountPools(t *testing.T) {
	pool := newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyWeightedRoundRobin, Accounts: []account{{Key: "a", AuthName: "a.json", Provider: "openai", Weight: 1}}})
	plans := []comboPlan{{aliasKey: "combo", groups: []groupPlan{{groupKey: "primary", candidates: []candidatePlan{{candidateKey: "combo\x00primary\x00model", accountPool: pool}}}}}}
	runtime := newComboRuntime(time.Now)
	runtime.accountCurrent[pool.signature] = map[string]int{"a": 3}

	cloned := runtime.Clone(plans, time.Now)
	if got := cloned.accountCurrent[pool.signature]["a"]; got != 3 {
		t.Fatalf("preserved pool weight = %d, want 3", got)
	}
	cloned.accountCurrent[pool.signature]["a"] = 9
	if got := runtime.accountCurrent[pool.signature]["a"]; got != 3 {
		t.Fatalf("clone mutated original pool weight = %d", got)
	}

	changed := newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyWeightedRoundRobin, Accounts: []account{{Key: "a", AuthName: "a.json", Provider: "openai", Weight: 2}}})
	changedPlans := []comboPlan{{aliasKey: "combo", groups: []groupPlan{{groupKey: "primary", candidates: []candidatePlan{{candidateKey: "combo\x00primary\x00model", accountPool: changed}}}}}}
	if got := runtime.Clone(changedPlans, time.Now).accountCurrent[changed.signature]; got != nil {
		t.Fatalf("changed pool state retained = %#v", got)
	}
}

func TestComboHealthCooldownCircuitAndHalfOpenTransitions(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 5, "a"))
	plan := plugin.plans[0]
	group := plan.groups[0]
	candidate := group.candidates[0]
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	plugin.comboRuntime.now = func() time.Time { return now }
	resilience := resilienceConfig{
		CircuitFailureThreshold: 2,
		CircuitOpenSeconds:      2,
		CircuitMaxOpenSeconds:   8,
	}

	plugin.comboRuntime.MarkFailure(plan, group, candidate, resilience)
	health := plugin.comboRuntime.groups[runtimeGroupKey(plan.aliasKey, group.groupKey)].health[candidate.candidateKey]
	if health.consecutiveFailures != 1 || !health.coolUntil.Equal(now.Add(5*time.Second)) || health.circuitOpen {
		t.Fatalf("first failure health = %#v", health)
	}
	group.cooldownSec = 0

	now = now.Add(time.Second)
	plugin.comboRuntime.MarkFailure(plan, group, candidate, resilience)
	if !health.circuitOpen || !health.circuitOpenUntil.Equal(now.Add(2*time.Second)) {
		t.Fatalf("threshold failure health = %#v", health)
	}

	now = health.circuitOpenUntil
	selection := plugin.comboRuntime.selectFromGroup(plan, group, newAttemptLedger(1, 0))
	if selection.candidate.candidateKey != candidate.candidateKey || !health.halfOpenOccupied {
		t.Fatalf("half-open selection = %#v, health = %#v", selection, health)
	}

	plugin.comboRuntime.MarkFailure(plan, group, candidate, resilience)
	if health.halfOpenOccupied || health.circuitReopenCount != 1 || !health.circuitOpenUntil.Equal(now.Add(4*time.Second)) {
		t.Fatalf("reopened circuit health = %#v", health)
	}

	plugin.comboRuntime.MarkSuccess(plan, group, candidate)
	if health.consecutiveFailures != 0 || health.circuitOpen || health.halfOpenOccupied || !health.coolUntil.IsZero() {
		t.Fatalf("successful probe health = %#v", health)
	}
}

func TestComboHealthReleaseProbeKeepsCircuitOpen(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 0, "a"))
	plan := plugin.plans[0]
	group := plan.groups[0]
	candidate := group.candidates[0]
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	plugin.comboRuntime.now = func() time.Time { return now }
	resilience := resilienceConfig{CircuitFailureThreshold: 1, CircuitOpenSeconds: 2, CircuitMaxOpenSeconds: 8}

	plugin.comboRuntime.MarkFailure(plan, group, candidate, resilience)
	health := plugin.comboRuntime.groups[runtimeGroupKey(plan.aliasKey, group.groupKey)].health[candidate.candidateKey]
	now = health.circuitOpenUntil
	plugin.comboRuntime.selectFromGroup(plan, group, newAttemptLedger(1, 0))
	plugin.comboRuntime.ReleaseProbe(plan, group, candidate)
	if !health.circuitOpen || health.halfOpenOccupied || health.circuitReopenCount != 0 {
		t.Fatalf("released probe health = %#v", health)
	}
}
