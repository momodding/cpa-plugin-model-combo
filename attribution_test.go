package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestAttributionTrackerRoutedDirectAndConflict(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := newAttributionTracker(func() time.Time { return now })
	headers := http.Header{"Authorization": {"Bearer sk-client-ab"}}

	tracker.MarkRouted("auto", "openai/gpt-5.4(high)", headers)
	routed := tracker.Match(pluginapi.UsageRecord{Model: "openai/gpt-5.4", Alias: "gpt-5.4(high)", APIKey: "sk-client-ab", RequestedAt: now})
	if routed.Kind != attributionRouted || routed.RouterModel != "auto" {
		t.Fatalf("routed attribution = %#v", routed)
	}

	tracker.MarkDirect(pluginapi.ModelRouteRequest{RequestedModel: "gpt-5.4", Headers: headers})
	direct := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", APIKey: "sk-client-ab", RequestedAt: now})
	if direct.Kind != attributionDirect || direct.RouterModel != "" {
		t.Fatalf("direct attribution = %#v", direct)
	}

	tracker.MarkRouted("auto", "gpt-5.4", headers)
	tracker.MarkDirect(pluginapi.ModelRouteRequest{RequestedModel: "gpt-5.4", Headers: headers})
	conflict := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", APIKey: "sk-client-ab", RequestedAt: now})
	if conflict.Kind != attributionUnresolved {
		t.Fatalf("conflicting attribution = %#v", conflict)
	}
}

func TestAttributionTrackerJoinsLogicalAccountDigestByAuthID(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := newAttributionTracker(func() time.Time { return now })
	tracker.setAccountDigestKey(testAccountDigestKey)
	tracker.MarkRoutedWithAccount("auto", "gpt-5.4", nil, resolvedAccount{key: "pool-a", id: "opaque-auth-a"})

	if got := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", AuthID: "opaque-auth-b", RequestedAt: now}); got.Kind != attributionUnresolved {
		t.Fatalf("wrong account attribution = %#v", got)
	}
	got := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", AuthID: "opaque-auth-a", RequestedAt: now})
	if got.Kind != attributionRouted || got.RouterModel != "auto" {
		t.Fatalf("matching account attribution = %#v", got)
	}
	if got.AccountDigest == "" {
		t.Fatal("matching attribution lacks account digest")
	}
	for _, raw := range []string{"pool-a", "opaque-auth-a", "pool-a\x00opaque-auth-a"} {
		if strings.Contains(got.AccountDigest, raw) {
			t.Fatalf("account digest exposes %q: %#v", raw, got)
		}
	}
}

func TestAttributionDigestDistinguishesLogicalAccounts(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := newAttributionTracker(func() time.Time { return now })
	tracker.setAccountDigestKey(testAccountDigestKey)
	tracker.MarkRoutedWithAccount("auto", "gpt-5.4", nil, resolvedAccount{key: "pool-a", id: "auth-1"})
	first := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", AuthID: "auth-1", RequestedAt: now})
	tracker.MarkRoutedWithAccount("auto", "gpt-5.4", nil, resolvedAccount{key: "pool-b", id: "auth-1"})
	second := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", AuthID: "auth-1", RequestedAt: now})
	if first.AccountDigest == "" || second.AccountDigest == "" || first.AccountDigest == second.AccountDigest {
		t.Fatalf("digests not distinct per logical account: %#v %#v", first, second)
	}
}

func TestStoredUsageRecordKeepsOnlyAccountDigest(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := newAttributionTracker(func() time.Time { return now })
	tracker.setAccountDigestKey(testAccountDigestKey)
	tracker.MarkRoutedWithAccount("auto", "gpt-5.4", nil, resolvedAccount{key: "pool-secret", id: "auth-secret"})
	record := pluginapi.UsageRecord{Model: "gpt-5.4", AuthID: "auth-secret", AuthIndex: "index-secret", RequestedAt: now}
	result := tracker.Match(record)
	stored, err := json.Marshal(storedRecordFromUsage(record, result))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"account_digest":"`+result.AccountDigest+`"`) {
		t.Fatalf("stored usage lacks account digest: %s", stored)
	}
	for _, secret := range []string{"pool-secret", "auth-secret", "index-secret"} {
		if strings.Contains(string(stored), secret) {
			t.Fatalf("stored usage leaks %q: %s", secret, stored)
		}
	}
}

func TestAttributionTrackerRequiresCredentialAndUnanimousRoute(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := newAttributionTracker(func() time.Time { return now })
	tracker.MarkRouted("auto", "gpt-5.4", http.Header{"X-Api-Key": {"key-one"}})
	if got := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", APIKey: "key-two", RequestedAt: now}); got.Kind != attributionUnresolved {
		t.Fatalf("credential mismatch = %#v", got)
	}
	tracker.MarkRouted("auto", "gpt-5.4", nil)
	tracker.MarkRouted("cheap", "gpt-5.4", nil)
	if got := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", RequestedAt: now}); got.Kind != attributionUnresolved {
		t.Fatalf("route disagreement = %#v", got)
	}
}

func TestAttributionTrackerMatchesTimestampLessUsageAfterWindow(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := newAttributionTracker(func() time.Time { return now })
	tracker.MarkRouted("auto", "gpt-5.4", http.Header{"X-Api-Key": {"key-one"}})
	now = now.Add(attributionWindow + time.Second)
	got := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", APIKey: "key-one"})
	if got.Kind != attributionRouted || got.RouterModel != "auto" {
		t.Fatalf("timestamp-less attribution = %#v", got)
	}
}

func TestAttributionTrackerLeavesAmbiguousTimestampLessUsageUnattributed(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := newAttributionTracker(func() time.Time { return now })
	headers := http.Header{"X-Api-Key": {"key-one"}}
	tracker.MarkRouted("first", "gpt-5.4", headers)
	now = now.Add(time.Second)
	tracker.MarkRouted("second", "gpt-5.4", headers)
	if got := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", APIKey: "key-one"}); got.Kind != attributionUnresolved {
		t.Fatalf("ambiguous timestamp-less attribution = %#v", got)
	}
	if len(tracker.markers) != 2 {
		t.Fatalf("active markers after ambiguous match = %#v", tracker.markers)
	}
}

func TestAttributionTrackerExpiresAndCapsMarkers(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := newAttributionTracker(func() time.Time { return now })
	tracker.MarkRouted("old", "model", nil)
	now = now.Add(attributionRetention + time.Second)
	tracker.MarkRouted("new", "model", nil)
	if len(tracker.markers) != 1 || tracker.markers[0].routerModel != "new" {
		t.Fatalf("expired markers = %#v", tracker.markers)
	}
	for index := 0; index < maxAttributionMarks+10; index++ {
		tracker.MarkDirect(pluginapi.ModelRouteRequest{RequestedModel: "model"})
	}
	if len(tracker.markers) != maxAttributionMarks {
		t.Fatalf("marker count = %d", len(tracker.markers))
	}
}

func TestMaskAPIKey(t *testing.T) {
	for input, want := range map[string]string{"sk-client-ab": "sk******ab", "abcde": "******", "abcd": "******", "": ""} {
		if got := maskAPIKey(input); got != want {
			t.Fatalf("maskAPIKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStoredUsageRecordOmitsAccountIdentifiers(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	record := pluginapi.UsageRecord{
		Provider:    "openai",
		Model:       "gpt-5.4",
		APIKey:      "sk-client-secret",
		AuthID:      "opaque-auth-id-secret",
		AuthIndex:   "auth-index-secret",
		RequestedAt: now,
	}
	stored, err := json.Marshal(storedRecordFromUsage(record, attributionResult{Kind: attributionRouted, RouterModel: "auto"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"opaque-auth-id-secret", "auth-index-secret", "sk-client-secret"} {
		if strings.Contains(string(stored), secret) {
			t.Fatalf("stored usage leaks %q: %s", secret, stored)
		}
	}
}
