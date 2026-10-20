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
	runtime.accountCooldown[pool.signature] = map[string]time.Time{"a\x00auth-a": time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)}

	cloned := runtime.Clone(plans, time.Now)
	if got := cloned.accountCurrent[pool.signature]["a"]; got != 3 {
		t.Fatalf("preserved pool weight = %d, want 3", got)
	}
	if got := cloned.accountCooldown[pool.signature]["a\x00auth-a"]; !got.Equal(runtime.accountCooldown[pool.signature]["a\x00auth-a"]) {
		t.Fatalf("preserved pool cooldown = %s, want %s", got, runtime.accountCooldown[pool.signature]["a\x00auth-a"])
	}
	cloned.accountCurrent[pool.signature]["a"] = 9
	cloned.accountCooldown[pool.signature]["a\x00auth-a"] = time.Time{}
	if got := runtime.accountCurrent[pool.signature]["a"]; got != 3 {
		t.Fatalf("clone mutated original pool weight = %d", got)
	}
	if got := runtime.accountCooldown[pool.signature]["a\x00auth-a"]; got.IsZero() {
		t.Fatal("clone mutated original pool cooldown")
	}

	changed := newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyWeightedRoundRobin, Accounts: []account{{Key: "a", AuthName: "a.json", Provider: "openai", Weight: 2}}})
	changedPlans := []comboPlan{{aliasKey: "combo", groups: []groupPlan{{groupKey: "primary", candidates: []candidatePlan{{candidateKey: "combo\x00primary\x00model", accountPool: changed}}}}}}
	changedRuntime := runtime.Clone(changedPlans, time.Now)
	if got := changedRuntime.accountCurrent[changed.signature]; got != nil {
		t.Fatalf("changed pool state retained = %#v", got)
	}
	if got := changedRuntime.accountCooldown[changed.signature]; got != nil {
		t.Fatalf("changed pool cooldown retained = %#v", got)
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
	if health.ConsecutiveFailures != 1 || !health.CoolUntil.Equal(now.Add(5*time.Second)) || health.CircuitOpen {
		t.Fatalf("first failure health = %#v", health)
	}
	group.cooldownSec = 0

	now = now.Add(time.Second)
	plugin.comboRuntime.MarkFailure(plan, group, candidate, resilience)
	if !health.CircuitOpen || !health.CircuitOpenUntil.Equal(now.Add(2*time.Second)) {
		t.Fatalf("threshold failure health = %#v", health)
	}

	now = health.CircuitOpenUntil
	selection := plugin.comboRuntime.selectFromGroup(plan, group, newAttemptLedger(1, 0))
	if selection.candidate.candidateKey != candidate.candidateKey || !health.HalfOpenOccupied {
		t.Fatalf("half-open selection = %#v, health = %#v", selection, health)
	}

	plugin.comboRuntime.MarkFailure(plan, group, candidate, resilience)
	if health.HalfOpenOccupied || health.CircuitReopenCount != 1 || !health.CircuitOpenUntil.Equal(now.Add(4*time.Second)) {
		t.Fatalf("reopened circuit health = %#v", health)
	}

	plugin.comboRuntime.MarkSuccess(plan, group, candidate)
	if health.ConsecutiveFailures != 0 || health.CircuitOpen || health.HalfOpenOccupied || !health.CoolUntil.IsZero() {
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
	now = health.CircuitOpenUntil
	plugin.comboRuntime.selectFromGroup(plan, group, newAttemptLedger(1, 0))
	plugin.comboRuntime.ReleaseProbe(plan, group, candidate)
	if !health.CircuitOpen || health.HalfOpenOccupied || health.CircuitReopenCount != 0 {
		t.Fatalf("released probe health = %#v", health)
	}
}

func TestResetComboGroupHealthAndPersistWaitsForMutationCallback(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 0, "a"))
	plan := plugin.plans[0]
	group := plan.groups[0]
	candidate := group.candidates[0]
	runtime := plugin.comboRuntime

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackDone := make(chan struct{})
	events := make([]string, 0, 3)
	var postResetSnapshot map[string]map[string]map[string]*candidateHealth
	runtime.onMutate = func(snapshot map[string]map[string]map[string]*candidateHealth) {
		events = append(events, "mutation")
		if len(events) != 1 {
			postResetSnapshot = snapshot
			return
		}
		close(callbackStarted)
		<-releaseCallback
		close(callbackDone)
	}

	mutationDone := make(chan struct{})
	go func() {
		runtime.MarkFailure(plan, group, candidate, resilienceConfig{CircuitFailureThreshold: 1})
		close(mutationDone)
	}()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("mutation callback did not start")
	}

	deleteStarted := make(chan struct{})
	resetDone := make(chan error, 1)
	go func() {
		resetDone <- runtime.ResetComboGroupHealthAndPersist(plan.aliasKey, group.groupKey, func() error {
			events = append(events, "delete")
			close(deleteStarted)
			return nil
		})
	}()
	select {
	case <-deleteStarted:
		t.Fatal("reset delete ran before earlier mutation callback finished")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseCallback)
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("mutation callback did not finish")
	}
	select {
	case <-mutationDone:
	case <-time.After(time.Second):
		t.Fatal("mutation did not finish")
	}
	select {
	case <-deleteStarted:
	case <-time.After(time.Second):
		t.Fatal("reset delete did not run")
	}
	if err := <-resetDone; err != nil {
		t.Fatalf("reset: %v", err)
	}
	if health := runtime.groups[runtimeGroupKey(plan.aliasKey, group.groupKey)].health; len(health) != 0 {
		t.Fatalf("runtime health after reset = %#v", health)
	}
	runtime.MarkFailure(plan, group, candidate, resilienceConfig{CircuitFailureThreshold: 1})
	if got := events; len(got) != 3 || got[0] != "mutation" || got[1] != "delete" || got[2] != "mutation" {
		t.Fatalf("persistence order = %#v", got)
	}
	postResetHealth := postResetSnapshot[plan.aliasKey][group.groupKey][candidate.candidateKey]
	if postResetHealth == nil || postResetHealth.ConsecutiveFailures != 1 {
		t.Fatalf("post-reset persisted health = %#v", postResetSnapshot)
	}
}

func TestRestoreHealthResetsHalfOpenOccupiedAndAllowsSelection(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 0, "a"))
	plan := plugin.plans[0]
	group := plan.groups[0]
	candidate := group.candidates[0]
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	plugin.comboRuntime.now = func() time.Time { return now }

	// Create snapshot with CircuitOpen=true and HalfOpenOccupied=true
	snapshot := map[string]map[string]*candidateHealth{
		runtimeGroupKey(plan.aliasKey, group.groupKey): {
			candidate.candidateKey: {
				CircuitOpen:        true,
				CircuitOpenUntil:   now.Add(-1 * time.Second), // expired
				HalfOpenOccupied:   true,                      // stale from previous process
				CircuitReopenCount: 1,
			},
		},
	}
	plugin.comboRuntime.RestoreHealth(snapshot)

	// selectFromGroup must select the candidate (half-open probe allowed)
	selection := plugin.comboRuntime.selectFromGroup(plan, group, newAttemptLedger(1, 0))
	if selection.candidate.candidateKey != candidate.candidateKey {
		t.Fatalf("after restore, selectFromGroup failed to select half-open candidate, got selection = %#v", selection)
	}

	// health.HalfOpenOccupied must be true after selection (selectFromGroup sets it)
	gkey := runtimeGroupKey(plan.aliasKey, group.groupKey)
	health := plugin.comboRuntime.groups[gkey].health[candidate.candidateKey]
	if !health.HalfOpenOccupied {
		t.Fatalf("after selection, HalfOpenOccupied = false, want true")
	}
}

func TestRestoreHealthFromSnapshotDoesNotStrandCandidateWithHalfOpenOccupied(t *testing.T) {
	// Simulate process restart: SnapshotHealth captures circuit+half-open state,
	// RestoreHealth in new runtime must not strand candidate (resets HalfOpenOccupied).
	plugin1 := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 0, "a"))
	plan := plugin1.plans[0]
	group := plan.groups[0]
	candidate := group.candidates[0]
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	plugin1.comboRuntime.now = func() time.Time { return now }
	resilience := resilienceConfig{CircuitFailureThreshold: 1, CircuitOpenSeconds: 2, CircuitMaxOpenSeconds: 8}

	plugin1.comboRuntime.MarkFailure(plan, group, candidate, resilience)
	gkey := runtimeGroupKey(plan.aliasKey, group.groupKey)
	health := plugin1.comboRuntime.groups[gkey].health[candidate.candidateKey]
	circuitOpenUntil := health.CircuitOpenUntil

	// Advance time to circuit expiry and do half-open selection
	now = circuitOpenUntil
	plugin1.comboRuntime.now = func() time.Time { return now }
	plugin1.comboRuntime.selectFromGroup(plan, group, newAttemptLedger(1, 0))
	if !health.HalfOpenOccupied {
		t.Fatalf("after half-open selection, HalfOpenOccupied not set")
	}

	// Snapshot and persist
	storedSnapshot := plugin1.comboRuntime.SnapshotHealth()

	// Create new runtime (simulating process restart), restore from storage
	plugin2 := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 0, "a"))
	plugin2.comboRuntime.now = func() time.Time { return now }
	plugin2.comboRuntime.RestoreHealth(storedSnapshot)

	// selectFromGroup must select the candidate (RestoreHealth resets HalfOpenOccupied)
	selection2 := plugin2.comboRuntime.selectFromGroup(plan, group, newAttemptLedger(1, 0))
	if selection2.candidate.candidateKey != candidate.candidateKey {
		t.Fatalf("after restore from storage, selectFromGroup failed to select, got selection = %#v", selection2)
	}
}

func TestComboRuntimeSyncDropsStaleAccountState(t *testing.T) {
	pool := newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyWeightedRoundRobin, Accounts: []account{{Key: "a", AuthName: "a.json", Provider: "openai", Weight: 1}}})
	plans := []comboPlan{{aliasKey: "combo", groups: []groupPlan{{groupKey: "primary", candidates: []candidatePlan{{candidateKey: "combo\x00primary\x00model", accountPool: pool}}}}}}
	runtime := newComboRuntime(time.Now)
	runtime.Sync(plans)
	runtime.accountCurrent[pool.signature] = map[string]int{"a": 3}
	runtime.accountCooldown[pool.signature] = map[string]time.Time{"a\x00auth-a": time.Now().Add(time.Hour)}
	runtime.Sync(nil)
	runtime.Sync(plans)
	if got := runtime.accountCurrent[pool.signature]; got != nil {
		t.Fatalf("stale weight state retained = %#v", got)
	}
	if got := runtime.accountCooldown[pool.signature]; got != nil {
		t.Fatalf("stale cooldown state retained = %#v", got)
	}
}
