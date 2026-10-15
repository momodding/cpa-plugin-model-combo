package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type fakeModelHost struct {
	execute      func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error)
	start        func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error)
	listAuth     func() ([]pluginapi.HostAuthFileEntry, error)
	reads        map[string][]pluginapi.HostModelStreamReadResponse
	executeCalls []pluginapi.HostModelExecutionRequest
	startCalls   []pluginapi.HostModelExecutionRequest
	emitted      [][]byte
	closed       []string
}

func (host *fakeModelHost) ListAuth() ([]pluginapi.HostAuthFileEntry, error) {
	if host.listAuth == nil {
		return nil, nil
	}
	return host.listAuth()
}

func (host *fakeModelHost) Execute(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
	host.executeCalls = append(host.executeCalls, request)
	if host.execute == nil {
		return pluginapi.HostModelExecutionResponse{}, errors.New("unexpected Execute call")
	}
	return host.execute(request)
}

func (host *fakeModelHost) StartStream(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
	host.startCalls = append(host.startCalls, request)
	if host.start == nil {
		return pluginapi.HostModelStreamResponse{}, errors.New("unexpected StartStream call")
	}
	return host.start(request)
}

func (host *fakeModelHost) ReadStream(streamID string) (pluginapi.HostModelStreamReadResponse, error) {
	responses := host.reads[streamID]
	if len(responses) == 0 {
		return pluginapi.HostModelStreamReadResponse{}, errors.New("unexpected ReadStream call")
	}
	response := responses[0]
	host.reads[streamID] = responses[1:]
	return response, nil
}

func (host *fakeModelHost) CloseStream(streamID string) error {
	host.closed = append(host.closed, streamID)
	return nil
}

func (host *fakeModelHost) Emit(_ string, payload []byte) error {
	host.emitted = append(host.emitted, append([]byte(nil), payload...))
	return nil
}

func (*fakeModelHost) ClosePluginStream(string, string) {}

func TestExecuteWithHostFailsOverAndRewritesAlias(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a", "provider-b"))
	host := &fakeModelHost{}
	host.execute = func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		if request.Model == "provider-a(high)" {
			return pluginapi.HostModelExecutionResponse{StatusCode: 429, Body: []byte(`{"error":"rate limit"}`)}, nil
		}
		return pluginapi.HostModelExecutionResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"X-Upstream": []string{"provider-b"}},
			Body:       []byte(`{"id":"result","model":"provider-b(high)"}`),
		}, nil
	}
	request := pluginapi.ExecutorRequest{
		Model:           "smart(high)",
		SourceFormat:    "openai",
		OriginalRequest: []byte(`{"model":"smart(high)","messages":[]}`),
		Headers:         http.Header{"Authorization": []string{"Bearer client-secret"}, "X-Trace": []string{"trace-id"}},
		Query:           url.Values{"alt": []string{"json"}},
		Alt:             "sse",
	}
	response, err := plugin.executeWithHost(request, host)
	if err != nil {
		t.Fatalf("executeWithHost() error = %v", err)
	}
	if len(host.executeCalls) != 2 {
		t.Fatalf("Execute calls = %d, want 2", len(host.executeCalls))
	}
	for index, call := range host.executeCalls {
		if call.Headers.Get("Authorization") != "" || call.Headers.Get("X-Trace") != "trace-id" {
			t.Fatalf("call %d headers = %#v", index, call.Headers)
		}
		var body map[string]any
		if err := json.Unmarshal(call.Body, &body); err != nil {
			t.Fatalf("call %d body error = %v", index, err)
		}
		if body["model"] != call.Model {
			t.Fatalf("call %d body model = %v, request model = %q", index, body["model"], call.Model)
		}
		if call.EntryProtocol != "openai" || call.ExitProtocol != "openai" || call.Query.Get("alt") != "json" || call.Alt != "sse" {
			t.Fatalf("call %d protocol forwarding = %#v", index, call)
		}
	}
	if model := jsonModel(t, response.Payload); model != "smart(high)" {
		t.Fatalf("response model = %q", model)
	}
	if response.Headers.Get("X-Upstream") != "provider-b" || response.Metadata["selected_model"] != "provider-b(high)" {
		t.Fatalf("response = %#v", response)
	}
}

func TestExecuteWithHostAdvancesToNextComboGroup(t *testing.T) {
	plugin := testComboPluginFromConfig(t, `
combos:
  - alias: smart
    groups:
      - name: primary
        strategy: priority
        targets:
          - model: provider-a
      - name: fallback
        strategy: priority
        targets:
          - model: provider-b
`)
	host := &fakeModelHost{execute: func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		if request.Model == "provider-a" {
			return pluginapi.HostModelExecutionResponse{StatusCode: http.StatusServiceUnavailable}, nil
		}
		return pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"model":"provider-b"}`)}, nil
	}}
	response, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if err != nil {
		t.Fatalf("executeWithHost() error = %v", err)
	}
	if len(host.executeCalls) != 2 || host.executeCalls[0].Model != "provider-a" || host.executeCalls[1].Model != "provider-b" {
		t.Fatalf("ordered group calls = %#v", host.executeCalls)
	}
	if response.Metadata["group_name"] != "fallback" || jsonModel(t, response.Payload) != "smart" {
		t.Fatalf("response = %#v", response)
	}
}

// testComboPluginFromConfig builds a plugin through the real configuration
// parser, so a test can exercise the status lists the plugin is configured with.
func testComboPluginFromConfig(t *testing.T, raw string) *modelComboPlugin {
	t.Helper()
	cfg, err := decodeRouterConfig([]byte(raw))
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	plugin := testComboPlugin(cfg.Routes...)
	plugin.config = cfg
	plugin.plans = compileCombosWithPools(cfg.Combos, cfg.Resilience, cfg.AccountPools)
	plugin.comboRuntime = newComboRuntime(nil)
	plugin.comboRuntime.Sync(plugin.plans)
	return plugin
}

func TestExecuteWithHostPinsResolvedAccountID(t *testing.T) {
	plugin := testComboPluginFromConfig(t, `
account_pools:
  pool:
    accounts:
      - key: primary
        auth_name: primary
        provider: openai
combos:
  - alias: smart
    groups:
      - name: primary
        targets:
          - model: provider-a
            account_pool: pool
`)
	host := &fakeModelHost{
		listAuth: func() ([]pluginapi.HostAuthFileEntry, error) {
			return []pluginapi.HostAuthFileEntry{{Name: "primary", Provider: "openai", ID: "opaque-auth-id"}}, nil
		},
		execute: func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
			return pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"model":"provider-a"}`)}, nil
		},
	}
	if _, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host); err != nil {
		t.Fatalf("executeWithHost() error = %v", err)
	}
	if len(host.executeCalls) != 1 || host.executeCalls[0].AuthID != "opaque-auth-id" {
		t.Fatalf("execution requests = %#v", host.executeCalls)
	}
}

func TestExecuteWithHostRetriesAnotherPooledAccount(t *testing.T) {
	plugin := testComboPluginFromConfig(t, `
resilience: {max_attempts: 2, base_backoff_ms: 0, max_backoff_ms: 0}
account_pools:
  pool:
    strategy: priority
    accounts:
      - {key: first, auth_name: first, provider: openai}
      - {key: second, auth_name: second, provider: openai}
combos:
  - alias: smart
    groups:
      - name: primary
        targets: [{model: provider-a, account_pool: pool}]
`)
	host := &fakeModelHost{
		listAuth: func() ([]pluginapi.HostAuthFileEntry, error) {
			return []pluginapi.HostAuthFileEntry{{Name: "first", Provider: "openai", ID: "id-first"}, {Name: "second", Provider: "openai", ID: "id-second"}}, nil
		},
		execute: func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
			if request.AuthID == "id-first" {
				return pluginapi.HostModelExecutionResponse{StatusCode: http.StatusServiceUnavailable}, nil
			}
			return pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"model":"provider-a"}`)}, nil
		},
	}
	if _, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host); err != nil {
		t.Fatalf("executeWithHost() error = %v", err)
	}
	if len(host.executeCalls) != 2 || host.executeCalls[0].AuthID != "id-first" || host.executeCalls[1].AuthID != "id-second" {
		t.Fatalf("account retry requests = %#v", host.executeCalls)
	}
}

func TestExecuteWithHostDoesNotFailOverOnBadRequest(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a", "b"))
	rejected := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: 400, Body: []byte(`{"error":"invalid request"}`)}, nil
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, rejected)
	if statusFromError(err) != 400 {
		t.Fatalf("executeWithHost() error = %v, want the host status", err)
	}
	if len(rejected.executeCalls) != 1 {
		t.Fatalf("a rejected request must not be retried against the next target, calls = %d", len(rejected.executeCalls))
	}
	healthy := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"model":"a"}`)}, nil
	}}
	if _, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, healthy); err != nil {
		t.Fatalf("a valid request after a rejected one must still reach a provider: %v", err)
	}
	if len(healthy.executeCalls) != 1 {
		t.Fatalf("valid request calls = %d, want 1", len(healthy.executeCalls))
	}
	if model := healthy.executeCalls[0].Model; model != "a" {
		t.Fatalf("the rejected request must not cool the preferred target, valid request selected %q", model)
	}
}

func TestExecuteWithHostKeepsRouteAvailableAfterPersistedItemMiss(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a", "b"))
	miss := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: 404, Body: []byte(`{"error":"items are not persisted when store is false"}`)}, nil
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, miss)
	if statusFromError(err) != 404 || len(miss.executeCalls) != 1 {
		t.Fatalf("executeWithHost() error = %v, calls = %d", err, len(miss.executeCalls))
	}
	healthy := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"model":"a"}`)}, nil
	}}
	if _, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, healthy); err != nil {
		t.Fatalf("a persisted response miss must not cool the route: %v", err)
	}
	if len(healthy.executeCalls) != 1 {
		t.Fatalf("valid request calls = %d, want 1", len(healthy.executeCalls))
	}
	if model := healthy.executeCalls[0].Model; model != "a" {
		t.Fatalf("the persisted miss must not cool the preferred target, valid request selected %q", model)
	}
}

func TestExecuteWithHostFailsOverOnConfiguredStatus(t *testing.T) {
	plugin := testComboPluginFromConfig(t, `
fallback:
  fallback_on_status: [400]
routes:
  - alias: smart
    cooldown_seconds: 30
    targets:
      - model: a
      - model: b
`)
	host := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, statusError{status: http.StatusBadRequest, message: "invalid prompt"}
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if len(host.executeCalls) != 2 {
		t.Fatalf("a 400 listed in fallback_on_status must be retried, calls = %d", len(host.executeCalls))
	}
	if statusFromError(err) != 503 || codeFromError(err, "") != "model_combo_unavailable" {
		t.Fatalf("executeWithHost() error = %v", err)
	}
}

func TestExecuteWithHostDoesNotFailOverTerminalError(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a", "b"))
	host := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, context.Canceled
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if !errors.Is(err, context.Canceled) || len(host.executeCalls) != 1 {
		t.Fatalf("executeWithHost() error = %v, calls = %d", err, len(host.executeCalls))
	}
}

func TestExecuteWithHostContextCancelsRetryBackoff(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a", "b"))
	plugin.plans[0].resilience.BaseBackoffMilliseconds = 1_000
	plugin.plans[0].resilience.MaxBackoffMilliseconds = 1_000
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	host := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, statusError{status: http.StatusServiceUnavailable}
	}}
	_, err := plugin.executeWithHostContext(ctx, pluginapi.ExecutorRequest{Model: "smart"}, host)
	if !errors.Is(err, context.Canceled) || len(host.executeCalls) != 1 {
		t.Fatalf("executeWithHostContext() error = %v, calls = %d", err, len(host.executeCalls))
	}
}

func TestExecuteWithHostReturnsUnavailableAfterCandidatesFail(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a", "b"))
	host := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, statusError{status: 503, message: "provider unavailable"}
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if statusFromError(err) != 503 || codeFromError(err, "") != "model_combo_unavailable" || len(host.executeCalls) != 2 {
		t.Fatalf("executeWithHost() error = %v, calls = %d", err, len(host.executeCalls))
	}
	_, err = plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if statusFromError(err) != 429 || codeFromError(err, "") != "model_combo_cooldown" {
		t.Fatalf("cooldown error = %v", err)
	}
}

func TestExecuteWithHostAttemptsWeightedTargetOncePerRequest(t *testing.T) {
	route := modelRoute{
		Alias:           "weighted",
		Strategy:        routeStrategyRoundRobin,
		CooldownSeconds: 30,
		Targets: []modelTarget{
			{Model: "a", Weight: 3},
			{Model: "b", Weight: 1},
		},
	}
	plugin := testComboPlugin(route)
	host := &fakeModelHost{execute: func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, statusError{status: 503, message: request.Model + " unavailable"}
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "weighted"}, host)
	if statusFromError(err) != 503 || codeFromError(err, "") != "model_combo_unavailable" {
		t.Fatalf("executeWithHost() error = %v", err)
	}
	if len(host.executeCalls) != 2 || host.executeCalls[0].Model != "a" || host.executeCalls[1].Model != "b" {
		t.Fatalf("weighted failover calls = %#v, want a then b once", host.executeCalls)
	}
}

func TestCountTokensIsExplicitlyUnsupported(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a"))
	_, err := plugin.CountTokens(t.Context(), pluginapi.ExecutorRequest{Model: "smart"})
	if statusFromError(err) != 501 || codeFromError(err, "") != "model_route_count_tokens_unsupported" {
		t.Fatalf("CountTokens() error = %v", err)
	}
}

func TestSanitizeNestedHeaders(t *testing.T) {
	headers := sanitizeNestedHeaders(http.Header{
		"Authorization":    []string{"secret"},
		"X-Api-Key":        []string{"secret"},
		"X-Custom-Token":   []string{"secret"},
		"Proxy-Connection": []string{"keep-alive"},
		"X-Trace":          []string{"trace-id"},
	})
	if headers.Get("Authorization") != "" || headers.Get("X-Api-Key") != "" || headers.Get("X-Custom-Token") != "" {
		t.Fatalf("sensitive headers survived: %#v", headers)
	}
	if headers.Get("X-Trace") != "trace-id" {
		t.Fatalf("benign header missing: %#v", headers)
	}
}

func TestExecuteWithHostSkipsCapabilityIncompatibleTargets(t *testing.T) {
	plugin := testComboPluginFromConfig(t, `
combos:
  - alias: smart
    capability_policy: require-declared
    groups:
      - name: primary
        targets:
          - model: text-only
          - model: tool-capable
            capabilities: [tools]
`)
	host := &fakeModelHost{execute: func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"model":"tool-capable"}`)}, nil
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart", OriginalRequest: []byte(`{"model":"smart","tools":[{"type":"function"}]}`)}, host)
	if err != nil || len(host.executeCalls) != 1 || host.executeCalls[0].Model != "tool-capable" {
		t.Fatalf("executeWithHost() error = %v, calls = %#v", err, host.executeCalls)
	}

	plugin = testComboPluginFromConfig(t, `
combos:
  - alias: smart
    capability_policy: require-declared
    groups:
      - name: primary
        targets: [{model: text-only}]
`)
	host = &fakeModelHost{}
	_, err = plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart", OriginalRequest: []byte(`{"model":"smart","tools":[{"type":"function"}]}`)}, host)
	if statusFromError(err) != http.StatusServiceUnavailable || codeFromError(err, "") != "model_combo_unavailable" || len(host.executeCalls) != 0 {
		t.Fatalf("executeWithHost() error = %v, calls = %d", err, len(host.executeCalls))
	}
	plugin = testComboPluginFromConfig(t, `
combos:
  - alias: smart
    groups:
      - name: primary
        targets:
          - model: text-only
            capabilities: []
`)
	host = &fakeModelHost{}
	_, err = plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart", OriginalRequest: []byte(`{"model":"smart","tools":[{"type":"function"}]}`)}, host)
	if statusFromError(err) != http.StatusServiceUnavailable || codeFromError(err, "") != "model_combo_unavailable" || len(host.executeCalls) != 0 {
		t.Fatalf("explicit empty capabilities error = %v, calls = %d", err, len(host.executeCalls))
	}
}

func jsonModel(t *testing.T, raw []byte) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode JSON: %v; body = %s", err, raw)
	}
	model, _ := body["model"].(string)
	return strings.TrimSpace(model)
}

func TestHostRequestWithAuthPinsExactID(t *testing.T) {
	request := pluginapi.ExecutorRequest{Headers: http.Header{"X-Test": {"value"}}}
	got := hostRequestWithAuth(request, executionBody{}, "provider/model", false, "opaque-auth-id")
	if got.AuthID != "opaque-auth-id" {
		t.Fatalf("AuthID = %q, want exact pin", got.AuthID)
	}
}
