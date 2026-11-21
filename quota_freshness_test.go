package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestQuotaFailureAndExpirationUseConfiguredDefault(t *testing.T) {
	for _, scenario := range []string{"failed refresh", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
			var fail atomic.Bool
			var block atomic.Bool
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if block.Load() {
					<-release
				}
				if fail.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status_code": 200,
					"body":        `{"rate_limit":{"primary_window":{"used_percent":5}}}`,
				})
			}))
			defer server.Close()
			t.Setenv("MODEL_COMBO_MANAGEMENT_URL", server.URL)
			t.Setenv("MODEL_COMBO_MANAGEMENT_KEY", "test-key")
			cfg := defaultQuotaPollingConfig()
			cfg.DefaultUnknownQuotaPercent = 35
			manager := newQuotaManager(cfg)
			manager.now = func() time.Time { return time.Unix(0, clock.Load()) }
			defer manager.Reconcile(cfg, false)
			account := accountCredential{ID: "account", AuthIndex: "index", Provider: "codex"}
			known := accountCredential{ID: "known", AuthIndex: "known", Provider: "codex"}
			refresh := func() {
				t.Helper()
				manager.mu.Lock()
				key := quotaKey(account)
				entry := manager.entries[key]
				if entry == nil {
					entry = &quotaEntry{credential: account}
					manager.entries[key] = entry
				}
				manager.startFetchLocked(key, entry)
				flight := entry.flight
				manager.mu.Unlock()
				<-flight
			}
			plugin := &modelRouterPlugin{quota: manager, config: routerConfig{QuotaPolling: cfg}, runtime: newRouteRuntime(nil)}
			assertDiagnostic := func(wantState string, wantPercent float64, wantReason string) {
				t.Helper()
				items := quotaDiagnostics(plugin).(map[string]any)["credentials"].([]map[string]any)
				for _, item := range items {
					if item["auth_index"] == account.AuthIndex {
						if item["state"] != wantState || item["effective_percent"] != wantPercent || item["reason"] != wantReason {
							t.Fatalf("diagnostic: %#v", item)
						}
						return
					}
				}
				t.Fatalf("account missing from diagnostics: %#v", items)
			}
			refresh()
			assertDiagnostic("known", 95, "")
			if scenario == "failed refresh" {
				fail.Store(true)
				refresh()
				assertDiagnostic("unknown", 35, "management_request_failed")
			} else {
				clock.Add(int64(time.Duration(cfg.TTLSeconds+1) * time.Second))
				block.Store(true)
				assertDiagnostic("unknown", 35, "stale")
			}
			manager.setSnapshot(known, quotaSnapshot{State: quotaKnown, Windows: []quotaWindow{{Remaining: .4}}, FetchedAt: manager.now()})
			route := modelRoute{Alias: "route", Strategy: routeStrategyHeadroom, Targets: []modelTarget{{Model: "unknown"}, {Model: "known"}}}
			plugin.runtime.Sync([]modelRoute{route})
			accounts := map[string][]accountCredential{routeKey("unknown"): {account}, routeKey("known"): {known}}
			candidates, _ := plugin.headroomCandidates(pluginapi.ExecutorRequest{Model: "route"}, &fakeModelHost{}, route, accounts, nil)
			if scenario == "expired" {
				manager.mu.Lock()
				flight := manager.entries[quotaKey(account)].flight
				manager.mu.Unlock()
				close(release)
				if flight != nil {
					<-flight
				}
			}
			if len(candidates) != 2 || candidates[0].account.ID != known.ID || candidates[0].score != .4 || candidates[1].account.ID != account.ID || candidates[1].state != quotaUnknown || candidates[1].score != .35 {
				t.Fatalf("candidates = %#v", candidates)
			}
		})
	}
}
