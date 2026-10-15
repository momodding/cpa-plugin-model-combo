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
