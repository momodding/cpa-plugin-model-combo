package main

import (
	"math"
	"testing"
)

func TestParseQuotaProviders(t *testing.T) {
	claude := parseProviderQuota("claude", []byte(`{"five_hour":{"utilization":65},"seven_day":{"utilization":30}}`))
	if claude.State != quotaKnown || len(claude.Windows) != 2 || math.Abs(claude.Windows[0].Remaining-0.35) > 1e-9 {
		t.Fatalf("unexpected Claude quota: %#v", claude)
	}
	codex := parseProviderQuota("codex", []byte(`{"rate_limit":{"primary_window":{"used_percent":60,"reset_at":1900000000}}}`))
	if codex.State != quotaKnown || len(codex.Windows) != 1 || codex.Windows[0].Remaining != 0.4 {
		t.Fatalf("unexpected Codex quota: %#v", codex)
	}
	antigravity := parseProviderQuota("antigravity", []byte(`{"groups":[{"buckets":[{"bucketId":"gemini-pro","window":"weekly","remainingFraction":0.7}]}]}`))
	if antigravity.State != quotaKnown || len(antigravity.Windows) != 1 || antigravity.Windows[0].Scope != "gemini" {
		t.Fatalf("unexpected Antigravity quota: %#v", antigravity)
	}
	partial := parseProviderQuota("claude", []byte(`{"five_hour":{"utilization":50},"seven_day":{"utilization":"bad"}}`))
	score, state := headroom(partial, "claude-sonnet")
	if state != quotaPartial || score != 0.5 {
		t.Fatalf("unexpected partial score %v/%v", score, state)
	}
}
func TestQuotaScoresAndScopes(t *testing.T) {
	snapshot := quotaSnapshot{State: quotaKnown, Windows: []quotaWindow{{Scope: "opus", Remaining: .2}, {Scope: "sonnet", Remaining: .8}, {Remaining: .6}}}
	if score, state := headroom(snapshot, "claude-sonnet-4"); score != .6 || state != quotaKnown {
		t.Fatalf("sonnet score = %v, %v", score, state)
	}
	if score, state := headroom(snapshot, "claude-opus-4"); score != .2 || state != quotaKnown {
		t.Fatalf("opus score = %v, %v", score, state)
	}
	if _, state := headroom(quotaSnapshot{State: quotaKnown, Windows: []quotaWindow{{Remaining: 0}}}, "model"); state != quotaExhausted {
		t.Fatalf("exhausted state = %v", state)
	}
}
func TestQuotaManagementURLIsLoopbackOnly(t *testing.T) {
	t.Setenv("MODEL_COMBO_MANAGEMENT_URL", "http://example.com")
	if _, reason := quotaManagementURL(); reason != "invalid_management_url" {
		t.Fatalf("expected loopback rejection, got %q", reason)
	}
	t.Setenv("MODEL_COMBO_MANAGEMENT_URL", "http://127.0.0.1:8317")
	if _, reason := quotaManagementURL(); reason != "" {
		t.Fatalf("expected local URL, got %q", reason)
	}
}
func TestQuotaUpstreamAllowlist(t *testing.T) {
	for _, endpoint := range []string{quotaClaudeURL, quotaCodexURL, quotaAntigravityURLs[0]} {
		if !quotaAllowedEndpoint(endpoint) {
			t.Fatalf("expected allowed: %s", endpoint)
		}
	}
	for _, endpoint := range []string{"http://127.0.0.1/admin", quotaClaudeURL + "?redirect=x", "https://api.anthropic.com.evil.test/api/oauth/usage"} {
		if quotaAllowedEndpoint(endpoint) {
			t.Fatalf("unexpected allowed endpoint: %s", endpoint)
		}
	}
}
