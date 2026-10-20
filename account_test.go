package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestResolveAccountPoolFailsClosed(t *testing.T) {
	plan := newAccountPlan(accountPool{
		Name:     "pool",
		Strategy: routeStrategyPriority,
		Accounts: []account{{Key: "primary", AuthName: "primary", Provider: "openai", Weight: 1}},
	})
	entries := []pluginapi.HostAuthFileEntry{{Name: "primary", Provider: "openai", ID: "auth-1"}}
	resolved := resolveAccountPool(plan, entries)
	if len(resolved) != 1 || resolved[0].id != "auth-1" {
		t.Fatalf("resolveAccountPool() = %#v", resolved)
	}
	for _, entry := range []pluginapi.HostAuthFileEntry{
		{Name: "primary", Provider: "openai", Disabled: true, ID: "auth-1"},
		{Name: "primary", Provider: "openai", Unavailable: true, ID: "auth-1"},
		{Name: "primary", Provider: "openai", Status: "disabled", ID: "auth-1"},
		{Name: "primary", Provider: "openai", NextRetryAfter: time.Now().Add(time.Hour), ID: "auth-1"},
		{Name: "primary", Provider: "openai"},
	} {
		if got := resolveAccountPool(plan, []pluginapi.HostAuthFileEntry{entry}); len(got) != 0 {
			t.Fatalf("resolveAccountPool(%#v) = %#v, want no account", entry, got)
		}
	}
	if got := resolveAccountPool(plan, []pluginapi.HostAuthFileEntry{entries[0], entries[0]}); len(got) != 0 {
		t.Fatalf("ambiguous metadata resolved to %#v", got)
	}
}

func TestResolveAccountPoolMatchesExactMetadata(t *testing.T) {
	plan := newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyPriority, Accounts: []account{{Key: "primary", AuthName: "Primary", Provider: "OpenAI", Weight: 1}}})
	entries := []pluginapi.HostAuthFileEntry{{Name: "primary", Provider: "OpenAI", ID: "wrong-name"}, {Name: "Primary", Provider: "openai", ID: "wrong-provider"}}
	if got := resolveAccountPool(plan, entries); len(got) != 0 {
		t.Fatalf("non-exact metadata resolved to %#v", got)
	}
}

func TestSelectResolvedAccountUsesLowestAvailablePriority(t *testing.T) {
	plan := newAccountPlan(accountPool{
		Name: "pool", Strategy: routeStrategyPriority,
		Accounts: []account{
			{Key: "fallback", Priority: 10, Weight: 1},
			{Key: "primary", Priority: 0, Weight: 1},
		},
	})
	selected, ok := selectResolvedAccount(newComboRuntime(nil), plan, []resolvedAccount{{key: "fallback", id: "auth-fallback"}, {key: "primary", id: "auth-primary"}})
	if !ok || selected.key != "primary" || selected.id != "auth-primary" {
		t.Fatalf("selectResolvedAccount() = %#v, %v", selected, ok)
	}
	selected, ok = selectResolvedAccount(newComboRuntime(nil), plan, []resolvedAccount{{key: "fallback", id: "auth-fallback"}})
	if !ok || selected.key != "fallback" {
		t.Fatalf("fallback selectResolvedAccount() = %#v, %v", selected, ok)
	}
}

func TestSelectResolvedAccountUsesSmoothWeightedRoundRobin(t *testing.T) {
	plan := newAccountPlan(accountPool{Name: "weighted", Strategy: routeStrategyWeightedRoundRobin, Accounts: []account{
		{Key: "a", Priority: 0, Weight: 2},
		{Key: "b", Priority: 0, Weight: 1},
	}})
	available := []resolvedAccount{{key: "a", id: "auth-a"}, {key: "b", id: "auth-b"}}
	runtime := newComboRuntime(nil)
	var got []string
	for range 6 {
		selected, ok := selectResolvedAccount(runtime, plan, available)
		if !ok {
			t.Fatal("selectResolvedAccount() did not select an account")
		}
		got = append(got, selected.key)
	}
	want := []string{"a", "b", "a", "a", "b", "a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cycle = %v, want %v", got, want)
		}
	}
}

func TestSelectResolvedAccountKeepsRuntimeRotationIsolated(t *testing.T) {
	plan := newAccountPlan(accountPool{Name: "weighted", Strategy: routeStrategyWeightedRoundRobin, Accounts: []account{{Key: "a", Weight: 2}, {Key: "b", Weight: 1}}})
	available := []resolvedAccount{{key: "a", id: "auth-a"}, {key: "b", id: "auth-b"}}
	first, second := newComboRuntime(nil), newComboRuntime(nil)
	for _, runtime := range []*comboRuntime{first, second} {
		selected, ok := selectResolvedAccount(runtime, plan, available)
		if !ok || selected.key != "a" {
			t.Fatalf("first selection = %#v, %v", selected, ok)
		}
	}
	selected, ok := selectResolvedAccount(first, plan, available)
	if !ok || selected.key != "b" {
		t.Fatalf("second first-runtime selection = %#v, %v", selected, ok)
	}
}

func TestAccountCooldownExcludesExactLogicalKey(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rt := newComboRuntime(func() time.Time { return now })
	poolSig := "test-pool"
	available := []resolvedAccount{{key: "k1", id: "shared-id"}, {key: "k2", id: "shared-id"}}

	// Mark k1 + shared-id as cooled.
	rt.MarkAccountFailure(poolSig, "k1"+"\x00"+"shared-id", 100*time.Millisecond)

	// filterCooledAccounts must exclude exact k1\x00shared-id only, not k2\x00shared-id.
	filtered := filterCooledAccounts(rt, poolSig, available)
	if len(filtered) != 1 || filtered[0].key != "k2" {
		t.Fatalf("filterCooledAccounts should exclude only exact logical key; got %#v", filtered)
	}
	// Verify cooldown persists after unrelated selection.
	selectResolvedAccount(rt, newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyPriority, Accounts: []account{{Key: "k2", AuthName: "k2", Provider: "test", Weight: 1}}}), available)
	filtered2 := filterCooledAccounts(rt, poolSig, available)
	if len(filtered2) != 1 || filtered2[0].key != "k2" {
		t.Fatalf("cooldown should persist after unrelated selection; got %#v", filtered2)
	}
}

func TestAccountCooldownExpiresWithDeterministicClock(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rt := newComboRuntime(func() time.Time { return now })
	poolSig := "test-pool"
	available := []resolvedAccount{{key: "test", id: "id-1"}}

	// Mark account as cooled for 100ms.
	rt.MarkAccountFailure(poolSig, "test"+"\x00"+"id-1", 100*time.Millisecond)

	// Before expiry, cooldown excludes account.
	filtered := filterCooledAccounts(rt, poolSig, available)
	if len(filtered) != 0 {
		t.Fatalf("expected cooldown to exclude account; got %#v", filtered)
	}
	// Pool map should exist.
	if len(rt.accountCooldown[poolSig]) != 1 {
		t.Fatalf("expected pool cooldown map with 1 entry; got %d", len(rt.accountCooldown[poolSig]))
	}

	// Advance time past expiry.
	now = now.Add(150 * time.Millisecond)

	// After expiry, account is available and expired entry deleted.
	filtered = filterCooledAccounts(rt, poolSig, available)
	if len(filtered) != 1 || filtered[0].key != "test" {
		t.Fatalf("expected account to be available after expiry; got %#v", filtered)
	}
	// Pool map should be deleted when empty.
	if _, exists := rt.accountCooldown[poolSig]; exists {
		t.Fatalf("expected empty pool map to be deleted")
	}
	// Verify stale logical key not in resolved is swept and pool map deleted.
	rt.MarkAccountFailure(poolSig, "stale"+"\x00"+"id-2", 100*time.Millisecond)
	if len(rt.accountCooldown[poolSig]) != 1 {
		t.Fatalf("expected pool cooldown map with stale entry; got %d", len(rt.accountCooldown[poolSig]))
	}
	// Advance clock another 150ms so stale entry expires.
	now = now.Add(150 * time.Millisecond)
	filtered = filterCooledAccounts(rt, poolSig, []resolvedAccount{})
	if len(filtered) != 0 {
		t.Fatalf("expected empty resolved to return empty; got %#v", filtered)
	}
	// Pool map should be deleted after sweeping stale entry.
	if _, exists := rt.accountCooldown[poolSig]; exists {
		t.Fatalf("expected pool map to be deleted after sweeping all expired entries")
	}
}

func TestAccountPlanSignatureIsUnambiguous(t *testing.T) {
	left := newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyPriority, Accounts: []account{{Key: "a\x00b", AuthName: "c", Provider: "p", Weight: 1}}})
	right := newAccountPlan(accountPool{Name: "pool", Strategy: routeStrategyPriority, Accounts: []account{{Key: "a", AuthName: "b\x00c", Provider: "p", Weight: 1}}})
	if left.signature == right.signature {
		t.Fatal("distinct account plans share signature")
	}
}
