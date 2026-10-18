package main

import (
	"sync"
	"testing"
	"time"
)

func selectorPlan(alias string, groups ...modelGroup) comboPlan {
	resilience, err := normalizeResilience(nil)
	if err != nil {
		panic(err)
	}
	return compileCombo(modelCombo{Alias: alias, Groups: groups}, resilience)
}

func TestComboSelectorReservesOnlyOneConcurrentHalfOpenProbe(t *testing.T) {
	plan := selectorPlan("half-open", selectorGroup("g", routeStrategyPriority, 0, comboTarget{Model: "a", Weight: 1}))
	runtime := newComboRuntime(nil)
	runtime.Sync([]comboPlan{plan})
	key := plan.groups[0].candidates[0].candidateKey
	group := runtime.groups[runtimeGroupKey(plan.aliasKey, plan.groups[0].groupKey)]
	group.health[key] = &candidateHealth{circuitOpen: true, circuitOpenUntil: time.Now().Add(-time.Second)}

	start := make(chan struct{})
	selections := make(chan comboSelection, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			selections <- runtime.selectFromGroup(plan, plan.groups[0], newAttemptLedger(1, 0))
		}()
	}
	close(start)
	wg.Wait()
	close(selections)

	selected := 0
	for selection := range selections {
		if selection.candidate.model != "" {
			selected++
		}
	}
	if selected != 1 {
		t.Fatalf("concurrent half-open selections = %d, want 1", selected)
	}
}

func TestComboSelectorOnlyReservesSelectedHalfOpenCandidate(t *testing.T) {
	plan := selectorPlan("half-open", selectorGroup("g", routeStrategyPriority, 0, comboTarget{Model: "a", Weight: 1}, comboTarget{Model: "b", Weight: 1}))
	runtime := newComboRuntime(nil)
	runtime.Sync([]comboPlan{plan})
	group := runtime.groups[runtimeGroupKey(plan.aliasKey, plan.groups[0].groupKey)]
	for _, candidate := range plan.groups[0].candidates {
		group.health[candidate.candidateKey] = &candidateHealth{circuitOpen: true, circuitOpenUntil: time.Now().Add(-time.Second)}
	}

	selection := runtime.selectFromGroup(plan, plan.groups[0], newAttemptLedger(1, 0))
	if selection.candidate.model != "a" {
		t.Fatalf("selected candidate = %#v, want a", selection)
	}
	for _, candidate := range plan.groups[0].candidates {
		occupied := group.health[candidate.candidateKey].halfOpenOccupied
		if occupied != (candidate.candidateKey == selection.candidate.candidateKey) {
			t.Fatalf("candidate %q occupied = %t, want %t", candidate.model, occupied, candidate.candidateKey == selection.candidate.candidateKey)
		}
	}
}

func selectorGroup(name, strategy string, cooldown int, targets ...comboTarget) modelGroup {
	return modelGroup{Name: name, Strategy: strategy, CooldownSeconds: cooldown, Targets: targets}
}

func TestComboSelectorPriorityAndOrderedGroups(t *testing.T) {
	plan := selectorPlan("smart",
		selectorGroup("first", routeStrategyPriority, 30, comboTarget{Model: "a", Weight: 1}, comboTarget{Model: "b", Weight: 1}),
		selectorGroup("second", routeStrategyPriority, 30, comboTarget{Model: "c", Weight: 1}),
	)
	runtime := newComboRuntime(nil)
	runtime.Sync([]comboPlan{plan})
	ledger := newAttemptLedger(4, 0)

	first := runtime.selectFromGroup(plan, plan.groups[0], ledger)
	if first.candidate.model != "a" {
		t.Fatalf("first priority selection = %#v, want a", first)
	}
	ledger.markAttempted(first.candidate.candidateKey)
	second := runtime.selectFromGroup(plan, plan.groups[0], ledger)
	if second.candidate.model != "b" {
		t.Fatalf("second priority selection = %#v, want b", second)
	}
	ledger.markAttempted(second.candidate.candidateKey)
	if exhausted := runtime.selectFromGroup(plan, plan.groups[0], ledger); exhausted.candidate.model != "" || exhausted.allCooling {
		t.Fatalf("first group exhaustion = %#v", exhausted)
	}
	fallback := runtime.selectFromGroup(plan, plan.groups[1], ledger)
	if fallback.candidate.model != "c" {
		t.Fatalf("second group selection = %#v, want c", fallback)
	}
}

func TestComboSelectorRoundRobinStateIsIndependent(t *testing.T) {
	one := selectorPlan("one", selectorGroup("g", routeStrategyRoundRobin, 0, comboTarget{Model: "a", Weight: 1}, comboTarget{Model: "b", Weight: 1}))
	two := selectorPlan("two", selectorGroup("g", routeStrategyRoundRobin, 0, comboTarget{Model: "c", Weight: 1}, comboTarget{Model: "d", Weight: 1}))
	runtime := newComboRuntime(nil)
	runtime.Sync([]comboPlan{one, two})

	for _, want := range []struct {
		plan  comboPlan
		model string
	}{{one, "a"}, {two, "c"}, {one, "b"}, {two, "d"}} {
		got := runtime.selectFromGroup(want.plan, want.plan.groups[0], newAttemptLedger(1, 0))
		if got.candidate.model != want.model {
			t.Fatalf("selection for %s = %q, want %q", want.plan.alias, got.candidate.model, want.model)
		}
	}
}

func TestComboSelectorSmoothWeightedRoundRobin(t *testing.T) {
	plan := selectorPlan("weighted", selectorGroup("g", routeStrategyWeightedRoundRobin, 0,
		comboTarget{Model: "a", Weight: 3}, comboTarget{Model: "b", Weight: 1},
	))
	runtime := newComboRuntime(nil)
	runtime.Sync([]comboPlan{plan})
	want := []string{"a", "a", "b", "a"}
	for _, model := range want {
		got := runtime.selectFromGroup(plan, plan.groups[0], newAttemptLedger(1, 0))
		if got.candidate.model != model {
			t.Fatalf("smooth WRR selection = %q, want %q", got.candidate.model, model)
		}
	}
}

func TestComboSelectorSkipsAttemptedAndAdvancesPastCoolingGroup(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	plan := selectorPlan("smart",
		selectorGroup("primary", routeStrategyPriority, 30, comboTarget{Model: "a", Weight: 1}),
		selectorGroup("fallback", routeStrategyPriority, 30, comboTarget{Model: "b", Weight: 1}),
	)
	runtime := newComboRuntime(func() time.Time { return now })
	runtime.Sync([]comboPlan{plan})
	ledger := newAttemptLedger(2, 0)
	primary := runtime.selectFromGroup(plan, plan.groups[0], ledger)
	ledger.markAttempted(primary.candidate.candidateKey)
	runtime.MarkFailure(plan, plan.groups[0], primary.candidate, plan.resilience)
	if cooling := runtime.selectFromGroup(plan, plan.groups[0], newAttemptLedger(2, 0)); !cooling.allCooling {
		t.Fatalf("cooling primary = %#v", cooling)
	}
	fallback := runtime.selectFromGroup(plan, plan.groups[1], ledger)
	if fallback.candidate.model != "b" {
		t.Fatalf("fallback after primary cooling = %#v", fallback)
	}
}

func TestComboRuntimeClonePreservesOnlyUnchangedGroups(t *testing.T) {
	plan := selectorPlan("smart", selectorGroup("g", routeStrategyRoundRobin, 0, comboTarget{Model: "a", Weight: 1}, comboTarget{Model: "b", Weight: 1}))
	runtime := newComboRuntime(nil)
	runtime.Sync([]comboPlan{plan})
	if got := runtime.selectFromGroup(plan, plan.groups[0], newAttemptLedger(1, 0)); got.candidate.model != "a" {
		t.Fatalf("initial selection = %#v", got)
	}
	unchanged := runtime.Clone([]comboPlan{plan}, nil)
	if got := unchanged.selectFromGroup(plan, plan.groups[0], newAttemptLedger(1, 0)); got.candidate.model != "b" {
		t.Fatalf("unchanged clone selection = %#v", got)
	}
	changed := selectorPlan("smart", selectorGroup("g", routeStrategyRoundRobin, 0, comboTarget{Model: "a", Weight: 1}, comboTarget{Model: "c", Weight: 1}))
	reset := runtime.Clone([]comboPlan{changed}, nil)
	if got := reset.selectFromGroup(changed, changed.groups[0], newAttemptLedger(1, 0)); got.candidate.model != "a" {
		t.Fatalf("changed group selection = %#v", got)
	}
}

func TestGroupSignatureChangesWithAccountPool(t *testing.T) {
	first := selectorPlan("combo", selectorGroup("group", routeStrategyPriority, 0, comboTarget{Model: "model", Weight: 1}))
	second := selectorPlan("combo", selectorGroup("group", routeStrategyPriority, 0, comboTarget{Model: "model", Weight: 1}))
	first.groups[0].candidates[0].accountPool = newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyWeightedRoundRobin, Accounts: []account{{Key: "a", AuthName: "a.json", Provider: "codex", Weight: 1}}})
	second.groups[0].candidates[0].accountPool = newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyWeightedRoundRobin, Accounts: []account{{Key: "a", AuthName: "a.json", Provider: "codex", Weight: 2}}})
	if groupSignature(first.groups[0]) == groupSignature(second.groups[0]) {
		t.Fatal("group signature omitted account pool contents")
	}
}
