package main

import (
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"testing"
	"time"
)

func TestHeadroomCandidateSortUsesConfiguredUnknownScore(t *testing.T) {
	p := &modelRouterPlugin{config: routerConfig{QuotaPolling: quotaPollingConfig{DefaultUnknownQuotaPercent: 35}}, runtime: newRouteRuntime(nil)}
	p.runtime.Sync([]modelRoute{{Alias: "route", Strategy: routeStrategyHeadroom, Targets: []modelTarget{{Model: "unknown"}, {Model: "known"}}}})
	route := modelRoute{Alias: "route", Strategy: routeStrategyHeadroom, Targets: []modelTarget{{Model: "unknown"}, {Model: "known"}}}
	accounts := map[string][]accountCredential{
		routeKey("unknown"): {{ID: "u", AuthIndex: "u", Provider: "codex"}},
		routeKey("known"):   {{ID: "k", AuthIndex: "k", Provider: "codex"}},
	}
	p.quota = newQuotaManager(defaultQuotaPollingConfig())
	p.quota.setSnapshot(accounts[routeKey("known")][0], quotaSnapshot{State: quotaKnown, Windows: []quotaWindow{{Remaining: .4}}, FetchedAt: time.Now()})
	p.quota.setSnapshot(accounts[routeKey("unknown")][0], quotaSnapshot{Err: "poll_pending"})
	candidates, _ := p.headroomCandidates(pluginapi.ExecutorRequest{Model: "route"}, &fakeModelHost{}, route, accounts, nil)
	if len(candidates) != 2 || candidates[0].account.ID != "k" || candidates[1].score != .35 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if p.quota != nil {
		p.quota.Reconcile(quotaPollingConfig{}, false)
	}
}

func TestHeadroomConfiguredUnknownScore(t *testing.T) {
	if got := defaultQuotaPollingConfig().DefaultUnknownQuotaPercent; got != 50 {
		t.Fatalf("default score = %v", got)
	}
	if got, state := headroom(quotaSnapshot{State: quotaUnknown}, "model"); got != 0 || state != quotaUnknown {
		t.Fatalf("headroom unknown = %v, %v", got, state)
	}
}
