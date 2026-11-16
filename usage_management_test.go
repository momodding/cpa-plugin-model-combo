package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestUsageManagementOverviewRequestsPricesPreferencesAndReset(t *testing.T) {
	store, err := openUsageStore(filepath.Join(t.TempDir(), "usage.db"), 365)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Add(-time.Minute)
	if err := store.Record(storedUsageRecord{
		RequestedAt: now, Attribution: attributionRouted, RouterModel: "auto", ProviderModel: "gpt-5.4", Provider: "openai",
		Source: "openai", InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
	}); err != nil {
		t.Fatal(err)
	}
	plugin := &modelRouterPlugin{store: store, attribution: newAttributionTracker(nil)}
	query := url.Values{"from": {now.Add(-time.Hour).Format(time.RFC3339)}, "to": {now.Add(time.Hour).Format(time.RFC3339)}}

	overview := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/overview", Query: query})
	if overview.StatusCode != http.StatusOK || !strings.Contains(string(overview.Body), `"router_models"`) || !strings.Contains(string(overview.Body), `"auto"`) {
		t.Fatalf("overview = status %d body %s", overview.StatusCode, overview.Body)
	}
	requests := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/requests", Query: query})
	if requests.StatusCode != http.StatusOK || !strings.Contains(string(requests.Body), `"provider_model":"gpt-5.4"`) {
		t.Fatalf("requests = status %d body %s", requests.StatusCode, requests.Body)
	}

	priceBody := `{"revision":0,"prices":{"gpt-5.4":{"input":1,"output":2,"cache_read":0.5,"cache_creation":0.75,"accounting_mode":"input_includes_cache"}},"sync_settings":{"provider_priority":["openai"],"ignored_suffixes":["(high)"],"mappings":[]}}`
	prices := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodPut, Path: modelRouterUsageBasePath + "/prices", Body: []byte(priceBody)})
	if prices.StatusCode != http.StatusOK || !strings.Contains(string(prices.Body), `"revision":1`) {
		t.Fatalf("prices = status %d body %s", prices.StatusCode, prices.Body)
	}
	stale := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodPut, Path: modelRouterUsageBasePath + "/prices", Body: []byte(priceBody)})
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale price status = %d body %s", stale.StatusCode, stale.Body)
	}

	preferences := defaultDashboardPreferences()
	preferences.RequestPageSize = 25
	preferencesBody, _ := json.Marshal(preferences)
	savedPreferences := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodPut, Path: modelRouterUsageBasePath + "/preferences", Body: preferencesBody})
	if savedPreferences.StatusCode != http.StatusOK || !strings.Contains(string(savedPreferences.Body), `"request_page_size":25`) {
		t.Fatalf("preferences = status %d body %s", savedPreferences.StatusCode, savedPreferences.Body)
	}
	reset := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodPost, Path: modelRouterUsageBasePath + "/reset", Body: []byte(`{"confirm":"reset"}`)})
	if reset.StatusCode != http.StatusOK {
		t.Fatalf("reset = status %d body %s", reset.StatusCode, reset.Body)
	}
	book, _ := store.QueryPriceBook()
	loadedPreferences, _ := store.QueryPreferences()
	if book.Revision != 1 || loadedPreferences.RequestPageSize != 25 {
		t.Fatalf("reset removed settings: prices=%#v preferences=%#v", book, loadedPreferences)
	}
}

func TestDiagnosticFiltersPreserveIdentitiesAndOmitCredentials(t *testing.T) {
	for _, value := range []string{"", "auto", "ordinary/model", "gpt-5.4(high)", "claude-sonnet-4-5-20251101", "http_429", "priority"} {
		if got := diagnosticFilterValue(value); got != value {
			t.Errorf("ordinary filter %q became %q", value, got)
		}
	}
	for _, value := range []string{"sk-private-key", "Bearer private-token", "gsk_private", "https://user:password@example.test/models", "https://example.test/models?token=private", strings.Repeat("a", 64), strings.Repeat("x", 513),
		"03e6a7b0-693b-4baa-ad22-29f2ed6ec6a7",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJwcml2YXRlIn0.signature",
		"YWFhYWFhYWFh/YmJiYmJiYmJiYmJiYmJiYmJiYmJi",
		strings.Repeat("a", 31) + "_",
	} {
		if got := diagnosticFilterValue(value); !strings.HasPrefix(got, "[omitted:") {
			t.Errorf("credential filter %q was retained", value)
		}
	}
}

func TestUsageManagementDashboardQueryOptions(t *testing.T) {
	store, filter := usageQueryFixture(t)
	plugin := &modelRouterPlugin{store: store}
	query := url.Values{
		"from": {filter.From.Format(time.RFC3339Nano)}, "to": {filter.To.Format(time.RFC3339Nano)}, "granularity": {"minute"},
		"source": {"GATEWAY"}, "group_dimension": {"router_model"}, "group_sort": {"key"}, "group_order": {"asc"}, "group_offset": {"1"}, "group_limit": {"3"},
		"request_sort": {"cost"}, "request_order": {"asc"}, "request_offset": {"2"}, "request_limit": {"4"},
	}
	response := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/dashboard", Query: query})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("dashboard returned %d: %s", response.StatusCode, response.Body)
	}
	var dashboard usageDashboard
	if err := json.Unmarshal(response.Body, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.Overview.Summary.Requests != 48 || dashboard.Requests.Total != 48 || len(dashboard.Requests.Items) != 4 || dashboard.Requests.Offset != 2 || dashboard.Requests.Limit != 4 || dashboard.Groups.Dimension != "router_model" || dashboard.Groups.Offset != 1 || dashboard.Groups.Limit != 3 {
		t.Fatalf("query options were not applied: %#v", dashboard)
	}
	filter.Source = "GATEWAY"
	want, err := store.referenceUsageRequests(filter, "cost", "asc", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range dashboard.Requests.Items {
		if item.Sequence != want.Items[index].Sequence {
			t.Fatal("request ordering did not use the prefixed options")
		}
	}
	if dashboard.GeneratedAt != dashboard.Overview.GeneratedAt || dashboard.GeneratedAt != dashboard.Groups.GeneratedAt || dashboard.GeneratedAt != dashboard.Requests.GeneratedAt || dashboard.PriceBookRevision != dashboard.Requests.PriceBookRevision {
		t.Fatal("dashboard response has inconsistent snapshot metadata")
	}
	if _, err := store.SavePriceBook(saveModelPricesRequest{Revision: dashboard.PriceBookRevision, Prices: map[string]modelPrice{"model-a": {tokenRates: tokenRates{Input: 10}}}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	response = handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/dashboard", Query: query})
	if err := json.Unmarshal(response.Body, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.PriceBookRevision != 2 || dashboard.Requests.PriceBookRevision != 2 || dashboard.Overview.Costs.UnpricedRequests == 0 {
		t.Fatal("new price revision was not applied to the whole dashboard")
	}
}

func TestUsageRequestHierarchy(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	selection := newUsageRequestSelection(usagePageOptions{Sort: "time", Order: "asc", Limit: 10}, usageFilter{ProviderModel: "wanted"})
	selection.add(storedUsageRecord{Sequence: 2, TraceID: "trace", RequestedAt: now, ProviderModel: "other", TotalTokens: 3}, estimatedCost{TotalUSD: 2})
	selection.add(storedUsageRecord{Sequence: 1, TraceID: "trace", RequestedAt: now, ProviderModel: "wanted", TotalTokens: 5, Failed: true}, estimatedCost{TotalUSD: 1})
	selection.addSession(storedRequestSession{TraceID: "trace", LifecycleRequestID: "inbound", StartedAt: now, RouterModel: "auto", Outcome: "succeeded"})
	page := selection.finish(0, now)
	if page.SchemaVersion != usageManagementSchemaVersion || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("unexpected page: %#v", page)
	}
	parent := page.Items[0]
	if parent.TraceID != "trace" || parent.LifecycleRequestID != "inbound" || parent.Result != "success" || parent.TotalTokens != 8 || parent.EstimatedCost.TotalUSD != 3 || parent.Attribution != attributionRouted || len(parent.Attempts) != 2 || parent.Attempts[0].Sequence != 1 || parent.Attempts[1].Sequence != 2 {
		t.Fatalf("unexpected parent: %#v", parent)
	}

	selection = newUsageRequestSelection(usagePageOptions{Sort: "time", Order: "asc", Offset: 1, Limit: 1}, usageFilter{})
	selection.addSession(storedRequestSession{TraceID: "collision", LifecycleRequestID: "a", StartedAt: now, Outcome: "failed"})
	selection.addSession(storedRequestSession{TraceID: "collision", LifecycleRequestID: "b", StartedAt: now, Outcome: "rejected", HTTPStatus: 429})
	selection.add(storedUsageRecord{Sequence: 3, TraceID: "collision", RequestedAt: now, TotalTokens: 7}, estimatedCost{})
	page = selection.finish(0, now)
	if page.Total != 3 || len(page.Items) != 1 || page.Items[0].CorrelationWarning == "" || len(page.Items[0].Attempts) != 0 {
		t.Fatalf("ambiguous page: %#v", page)
	}
}

func TestUsageRequestLegacyAndLifecycleOnly(t *testing.T) {
	now := time.Now().UTC()
	selection := newUsageRequestSelection(usagePageOptions{Sort: "time", Order: "asc", Limit: 10}, usageFilter{})
	selection.add(storedUsageRecord{Sequence: 1, Attribution: attributionRouted, RouterRequestID: "legacy", RouterAttempt: 1, RequestedAt: now, TotalTokens: 2}, estimatedCost{})
	selection.add(storedUsageRecord{Sequence: 2, Attribution: attributionRouted, RouterRequestID: "legacy", RouterAttempt: 2, RequestedAt: now, TotalTokens: 3}, estimatedCost{})
	selection.addSession(storedRequestSession{TraceID: "legacy", LifecycleRequestID: "inbound", StartedAt: now.Add(time.Second), Outcome: "rejected", HTTPStatus: 429})
	page := selection.finish(0, now)
	if page.Total != 2 || len(page.Items) != 2 || len(page.Items[0].Attempts) != 2 || page.Items[0].TotalTokens != 5 || len(page.Items[1].Attempts) != 0 || page.Items[1].Result != "http_429" || page.Items[1].TotalTokens != 0 {
		t.Fatalf("legacy/lifecycle page: %#v", page)
	}
}

// Concurrent identical requests were stored with swapped heuristic attribution; exact unambiguous sessions
// must win in filters, overview and groups, while ambiguous or unmatched traces keep the stored heuristic.
func TestUsageAccountingPrefersExactSessionAttribution(t *testing.T) {
	store, err := openUsageStore(filepath.Join(t.TempDir(), "usage.db"), 365)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Add(-time.Minute)
	records := []storedUsageRecord{
		{TraceID: "aaaa0001", Attribution: attributionRouted, RouterModel: "auto"},
		{TraceID: "bbbb0002", Attribution: attributionDirect},
		{TraceID: "cccc0003", Attribution: attributionRouted, RouterModel: "stored"},
		{TraceID: "dddd0004", Attribution: attributionRouted, RouterModel: "heuristic"},
		{Attribution: attributionUnresolved},
	}
	for _, record := range records {
		record.RequestedAt, record.ProviderModel, record.TotalTokens = now, "gpt", 1
		if err := store.Record(record); err != nil {
			t.Fatal(err)
		}
	}
	for _, session := range []storedRequestSession{
		{TraceID: "aaaa0001", LifecycleRequestID: "a"},
		{TraceID: "bbbb0002", LifecycleRequestID: "b", RouterModel: "auto"},
		{TraceID: "cccc0003", LifecycleRequestID: "c1", RouterModel: "other"},
		{TraceID: "cccc0003", LifecycleRequestID: "c2"},
	} {
		session.StartedAt = now
		if err := store.RecordSession(session); err != nil {
			t.Fatal(err)
		}
	}

	groups, err := store.Groups(usageFilter{}, "router_model", "key", "asc", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]uint64)
	for _, group := range groups.Items {
		got[group.Key] = group.Requests
	}
	if len(got) != 5 || got["direct"] != 1 || got["auto"] != 1 || got["stored"] != 1 || got["heuristic"] != 1 || got[attributionUnresolved] != 1 {
		t.Fatalf("groups = %#v", got)
	}
	routed, err := store.Overview(usageFilter{Attribution: attributionRouted}, "hour")
	if err != nil || routed.Summary.Requests != 3 {
		t.Fatalf("routed overview = %d, %v; want auto, stored, heuristic", routed.Summary.Requests, err)
	}
	direct, err := store.Overview(usageFilter{Attribution: attributionDirect}, "hour")
	if err != nil || direct.Summary.Requests != 1 {
		t.Fatalf("direct overview = %d, %v; want only the exact direct trace", direct.Summary.Requests, err)
	}
	byModel, err := store.Overview(usageFilter{RouterModel: "auto"}, "hour")
	requests, requestErr := store.Requests(usageFilter{RouterModel: "auto"}, "time", "asc", 0, 10)
	if err != nil || requestErr != nil || byModel.Summary.Requests != 1 || requests.Total != 1 || requests.Items[0].TraceID != "bbbb0002" {
		t.Fatalf("router filter: overview %d (%v), requests %#v (%v)", byModel.Summary.Requests, err, requests, requestErr)
	}

	stored, err := store.referenceUsageRecords(usageFilter{})
	if err != nil || len(stored) != len(records) || stored[0].Attribution != attributionRouted || stored[0].RouterModel != "auto" || stored[1].Attribution != attributionDirect || stored[1].RouterModel != "" {
		t.Fatalf("stored records changed: %#v, %v", stored, err)
	}
}

func TestUsageManagementDashboardRejectsInvalidOptions(t *testing.T) {
	store, _ := usageQueryFixture(t)
	plugin := &modelRouterPlugin{store: store}
	for _, test := range []struct{ key, value string }{
		{"granularity", "week"}, {"from", "invalid"}, {"group_dimension", "api_key"}, {"group_sort", "time"}, {"group_order", "sideways"}, {"group_offset", "-1"}, {"group_limit", "501"},
		{"request_sort", "requests"}, {"request_order", "sideways"}, {"request_offset", "-1"}, {"request_limit", "0"},
	} {
		t.Run(test.key, func(t *testing.T) {
			response := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/dashboard", Query: url.Values{test.key: {test.value}}})
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("invalid option returned %d: %s", response.StatusCode, response.Body)
			}
		})
	}
	response := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodPost, Path: modelRouterUsageBasePath + "/dashboard"})
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported method returned %d", response.StatusCode)
	}
}

func TestUsageManagementNamespacesRouterAliasesAndAttribution(t *testing.T) {
	store, err := openUsageStore(filepath.Join(t.TempDir(), "usage.db"), 365)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	for _, record := range []storedUsageRecord{
		{RequestedAt: now, Attribution: attributionRouted, RouterModel: "direct", ProviderModel: "routed-direct"},
		{RequestedAt: now, Attribution: attributionDirect, ProviderModel: "provider-direct"},
		{RequestedAt: now, Attribution: attributionRouted, RouterModel: "unattributed", ProviderModel: "routed-unattributed"},
		{RequestedAt: now, Attribution: attributionUnresolved, ProviderModel: "provider-unattributed"},
	} {
		if err := store.Record(record); err != nil {
			t.Fatal(err)
		}
	}
	plugin := &modelRouterPlugin{store: store, attribution: newAttributionTracker(nil)}
	baseQuery := func() url.Values {
		return url.Values{"from": {now.Add(-time.Hour).Format(time.RFC3339)}, "to": {now.Add(time.Hour).Format(time.RFC3339)}}
	}
	for _, test := range []struct {
		name            string
		parameter       string
		value           string
		wantAttribution string
		wantRouterModel string
	}{
		{name: "direct alias", parameter: "router_model", value: "direct", wantAttribution: attributionRouted, wantRouterModel: "direct"},
		{name: "direct traffic", parameter: "attribution", value: attributionDirect, wantAttribution: attributionDirect},
		{name: "unattributed alias", parameter: "router_model", value: "unattributed", wantAttribution: attributionRouted, wantRouterModel: "unattributed"},
		{name: "unattributed traffic", parameter: "attribution", value: attributionUnresolved, wantAttribution: attributionUnresolved},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := baseQuery()
			query.Set(test.parameter, test.value)
			response := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/requests", Query: query})
			var page usageRequestPage
			if err := json.Unmarshal(response.Body, &page); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || page.Total != 1 || page.Items[0].Attribution != test.wantAttribution || page.Items[0].RouterModel != test.wantRouterModel {
				t.Fatalf("filtered requests = status %d page %#v", response.StatusCode, page)
			}
		})
	}

	overviewResponse := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/overview", Query: baseQuery()})
	var overview usageOverview
	if err := json.Unmarshal(overviewResponse.Body, &overview); err != nil {
		t.Fatal(err)
	}
	seen := make(map[usageRouterKey]uint64, len(overview.RouterModels))
	for _, item := range overview.RouterModels {
		seen[usageRouterKey{model: item.Model, attribution: item.Attribution}] = item.Requests
	}
	for _, key := range []usageRouterKey{
		{model: "direct", attribution: attributionRouted},
		{attribution: attributionDirect},
		{model: "unattributed", attribution: attributionRouted},
		{attribution: attributionUnresolved},
	} {
		if seen[key] != 1 {
			t.Fatalf("router overview buckets = %#v", seen)
		}
	}

	groupQuery := baseQuery()
	groupQuery.Set("dimension", "router_model")
	groupsResponse := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/groups", Query: groupQuery})
	var groups usageGroupPage
	if err := json.Unmarshal(groupsResponse.Body, &groups); err != nil {
		t.Fatal(err)
	}
	if groupsResponse.StatusCode != http.StatusOK || groups.Total != 4 {
		t.Fatalf("router groups = status %d page %#v", groupsResponse.StatusCode, groups)
	}
}

func TestUsageManagementRejectsBadQueriesBodiesAndMethods(t *testing.T) {
	store, err := openUsageStore(filepath.Join(t.TempDir(), "usage.db"), 365)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plugin := &modelRouterPlugin{store: store, attribution: newAttributionTracker(nil)}
	tests := []pluginapi.ManagementRequest{
		{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/overview", Query: url.Values{"from": {"bad"}}},
		{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/requests", Query: url.Values{"limit": {"501"}}},
		{Method: http.MethodPut, Path: modelRouterUsageBasePath + "/preferences", Body: []byte(`{"request_page_size":100,"surprise":true}`)},
		{Method: http.MethodPost, Path: modelRouterUsageBasePath + "/reset", Body: []byte(`{"confirm":"no"}`)},
	}
	for _, request := range tests {
		response := handleModelRouterManagement(plugin, request)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("request %#v status = %d body %s", request, response.StatusCode, response.Body)
		}
	}
	response := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodPost, Path: modelRouterUsageBasePath + "/overview"})
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d body %s", response.StatusCode, response.Body)
	}
}

func TestUsageOverviewListsResultsOutsideRequestPage(t *testing.T) {
	store, err := openUsageStore(filepath.Join(t.TempDir(), "usage.db"), 365)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	for _, record := range []storedUsageRecord{
		{RequestedAt: now.Add(-time.Minute), Attribution: attributionDirect, ProviderModel: "model", Failed: true, StatusCode: http.StatusTooManyRequests},
		{RequestedAt: now, Attribution: attributionDirect, ProviderModel: "model"},
	} {
		if err := store.Record(record); err != nil {
			t.Fatal(err)
		}
	}
	plugin := &modelRouterPlugin{store: store, attribution: newAttributionTracker(nil)}
	query := url.Values{
		"from":  {now.Add(-time.Hour).Format(time.RFC3339)},
		"to":    {now.Add(time.Hour).Format(time.RFC3339)},
		"limit": {"1"},
	}
	requests := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/requests", Query: query})
	if !strings.Contains(string(requests.Body), `"result":"success"`) || strings.Contains(string(requests.Body), `"result":"http_429"`) {
		t.Fatalf("first request page = %s", requests.Body)
	}
	overview := handleModelRouterManagement(plugin, pluginapi.ManagementRequest{Method: http.MethodGet, Path: modelRouterUsageBasePath + "/overview", Query: query})
	var body struct {
		Results []string `json:"results"`
	}
	if err := json.Unmarshal(overview.Body, &body); err != nil {
		t.Fatal(err)
	}
	if strings.Join(body.Results, ",") != "http_429,success" {
		t.Fatalf("overview results = %#v", body.Results)
	}
}
