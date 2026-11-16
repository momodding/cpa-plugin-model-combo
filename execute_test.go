package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type fakeModelHost struct {
	execute      func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error)
	start        func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error)
	read         func(string) (pluginapi.HostModelStreamReadResponse, error)
	emit         func(string, []byte) error
	closePlugin  func(string, string)
	closeStream  func(string) error
	reads        map[string][]pluginapi.HostModelStreamReadResponse
	executeCalls []pluginapi.HostModelExecutionRequest
	startCalls   []pluginapi.HostModelExecutionRequest
	emitted      [][]byte
	closed       []string
}

type routingCatalogHost struct {
	*fakeModelHost
	models     []pluginapi.HostAuthModels
	entries    []pluginapi.HostAuthFileEntry
	catalogErr error
	modelCalls int
	listCalls  int
}

func (host *routingCatalogHost) AuthModels() ([]pluginapi.HostAuthModels, error) {
	host.modelCalls++
	return host.models, host.catalogErr
}

func (host *routingCatalogHost) AuthList() ([]pluginapi.HostAuthFileEntry, error) {
	host.listCalls++
	return host.entries, host.catalogErr
}

func routingTestHost() *routingCatalogHost {
	return &routingCatalogHost{fakeModelHost: &fakeModelHost{}, models: []pluginapi.HostAuthModels{
		{ID: "a", Provider: "one", Models: []string{"model-a"}},
		{ID: "b", Provider: "two", Models: []string{"model-a", "model-b"}},
		{ID: "c", Provider: "three", Models: []string{"model-a", "model-b"}},
	}}
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
	if host.read != nil {
		return host.read(streamID)
	}
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
	if host.closeStream != nil {
		return host.closeStream(streamID)
	}
	return nil
}

func (host *fakeModelHost) Emit(streamID string, payload []byte) error {
	host.emitted = append(host.emitted, append([]byte(nil), payload...))
	if host.emit != nil {
		return host.emit(streamID, payload)
	}
	return nil
}

func (host *fakeModelHost) ClosePluginStream(streamID, message string) {
	if host.closePlugin != nil {
		host.closePlugin(streamID, message)
	}
}

func TestExecuteWithHostAvailabilityFailures(t *testing.T) {
	for _, status := range []int{400, 401, 402, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			plugin := testRouterPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a", "provider-b"))
			host := &fakeModelHost{execute: func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
				if request.Model == "provider-a" {
					return pluginapi.HostModelExecutionResponse{StatusCode: status, Body: []byte(missingProviderErrorBody)}, nil
				}
				return pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: []byte(`{"model":"provider-b"}`)}, nil
			}}
			for range 2 {
				response, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
				if err != nil || jsonModel(t, response.Payload) != "smart" {
					t.Fatalf("response = %#v, error = %v", response, err)
				}
			}
			if len(host.executeCalls) != 3 || host.executeCalls[0].Model != "provider-a" || host.executeCalls[1].Model != "provider-b" || host.executeCalls[2].Model != "provider-b" {
				t.Fatalf("calls = %#v, want first target, fallback, then cooled-target skip", host.executeCalls)
			}
		})
	}
}

func TestExecuteWithHostMissingProvidersExhaustRoute(t *testing.T) {
	plugin := testRouterPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a", "provider-b"))
	host := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, statusError{status: 400, message: missingProviderErrorBody}
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if statusFromError(err) != 503 || codeFromError(err, "") != "model_route_unavailable" || len(host.executeCalls) != 2 {
		t.Fatalf("exhausted route error = %v, calls = %d", err, len(host.executeCalls))
	}
	_, err = plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if statusFromError(err) != 429 || codeFromError(err, "") != "model_route_cooldown" || len(host.executeCalls) != 2 {
		t.Fatalf("cooled route error = %v, calls = %d", err, len(host.executeCalls))
	}
}

func TestExecuteWithHostExplicitlyExcludesMissingProvider(t *testing.T) {
	plugin := testRouterPluginFromConfig(t, `fallback:
  no_fallback_on_status: [400]
routes:
  - alias: smart
    models: [provider-a, provider-b]
`)
	host := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, statusError{status: 400, message: missingProviderErrorBody}
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if statusFromError(err) != 400 || len(host.executeCalls) != 1 {
		t.Fatalf("excluded error = %v, calls = %d", err, len(host.executeCalls))
	}
	host.execute = func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: []byte(`{"model":"provider-a"}`)}, nil
	}
	if _, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host); err != nil || len(host.executeCalls) != 2 || host.executeCalls[1].Model != "provider-a" {
		t.Fatalf("excluded failure cooled preferred target: error = %v, calls = %#v", err, host.executeCalls)
	}
}

func TestExecuteWithHostFailsOverAndRewritesAlias(t *testing.T) {
	plugin := testRouterPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a", "provider-b"))
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
	}
	if model := jsonModel(t, response.Payload); model != "smart(high)" {
		t.Fatalf("response model = %q", model)
	}
	if response.Headers.Get("X-Upstream") != "provider-b" || response.Metadata["selected_model"] != "provider-b(high)" {
		t.Fatalf("response = %#v", response)
	}
}

// testRouterPluginFromConfig builds a plugin through the real configuration
// parser, so a test can exercise the status lists the plugin is configured with.
func testRouterPluginFromConfig(t *testing.T, raw string) *modelRouterPlugin {
	t.Helper()
	cfg, err := decodeRouterConfig([]byte(raw))
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	plugin := testRouterPlugin(cfg.Routes...)
	plugin.config = cfg
	return plugin
}

func TestExecuteWithHostDoesNotFailOverOnBadRequest(t *testing.T) {
	plugin := testRouterPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a", "b"))
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
	plugin := testRouterPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a", "b"))
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
	plugin := testRouterPluginFromConfig(t, `
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
	if statusFromError(err) != 503 || codeFromError(err, "") != "model_route_unavailable" {
		t.Fatalf("executeWithHost() error = %v", err)
	}
}

func TestExecuteWithHostDoesNotFailOverTerminalError(t *testing.T) {
	plugin := testRouterPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a", "b"))
	host := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, context.Canceled
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if !errors.Is(err, context.Canceled) || len(host.executeCalls) != 1 {
		t.Fatalf("executeWithHost() error = %v, calls = %d", err, len(host.executeCalls))
	}
}

func TestExecuteWithHostReturnsUnavailableAfterCandidatesFail(t *testing.T) {
	plugin := testRouterPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a", "b"))
	host := &fakeModelHost{execute: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, statusError{status: 503, message: "provider unavailable"}
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if statusFromError(err) != 503 || codeFromError(err, "") != "model_route_unavailable" || len(host.executeCalls) != 2 {
		t.Fatalf("executeWithHost() error = %v, calls = %d", err, len(host.executeCalls))
	}
	_, err = plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if statusFromError(err) != 429 || codeFromError(err, "") != "model_route_cooldown" {
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
	plugin := testRouterPlugin(route)
	host := &fakeModelHost{execute: func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{}, statusError{status: 503, message: request.Model + " unavailable"}
	}}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "weighted"}, host)
	if statusFromError(err) != 503 || codeFromError(err, "") != "model_route_unavailable" {
		t.Fatalf("executeWithHost() error = %v", err)
	}
	if len(host.executeCalls) != 2 || host.executeCalls[0].Model != "a" || host.executeCalls[1].Model != "b" {
		t.Fatalf("weighted failover calls = %#v, want a then b once", host.executeCalls)
	}
}

func TestRestrictedExecuteAccountFailover(t *testing.T) {
	route := testModelRoute("smart", routeStrategyPriority, 30, "model-a", "model-b")
	route.AllowedAccounts = []string{"a", "b"}
	plugin := testRouterPlugin(route)
	host := routingTestHost()
	host.execute = func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		if request.AuthID == "a" {
			return pluginapi.HostModelExecutionResponse{StatusCode: 429, Body: []byte("secret upstream body")}, nil
		}
		return pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: []byte(`{"model":"model-a(high)"}`)}, nil
	}
	for range 2 {
		if _, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart(high)"}, host); err != nil {
			t.Fatal(err)
		}
	}
	if len(host.executeCalls) != 4 || host.modelCalls != 2 || host.listCalls != 2 {
		t.Fatalf("calls = %#v, catalog calls = %d/%d", host.executeCalls, host.modelCalls, host.listCalls)
	}
	for i, call := range host.executeCalls {
		wantID, wantProvider := "a", "one"
		if i%2 == 1 {
			wantID, wantProvider = "b", "two"
		}
		if call.AuthID != wantID || call.ForcedProvider != wantProvider || call.Model != "model-a(high)" {
			t.Fatalf("call %d = %#v", i, call)
		}
	}
}

func TestRestrictedExecuteFallbackAndFailClosed(t *testing.T) {
	route := testModelRoute("smart", routeStrategyPriority, 30, "model-a", "model-b")
	route.AllowedAccounts = []string{"a", "b", "missing"}
	plugin := testRouterPlugin(route)
	host := routingTestHost()
	host.entries = []pluginapi.HostAuthFileEntry{{ID: "a", Disabled: true}}
	host.execute = func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		if request.Model == "model-a" {
			return pluginapi.HostModelExecutionResponse{StatusCode: 429}, nil
		}
		return pluginapi.HostModelExecutionResponse{StatusCode: 200}, nil
	}
	if _, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host); err != nil {
		t.Fatal(err)
	}
	if len(host.executeCalls) != 2 || host.executeCalls[0].AuthID != "b" || host.executeCalls[1].AuthID != "b" || host.executeCalls[1].Model != "model-b" {
		t.Fatalf("calls = %#v", host.executeCalls)
	}
	route.AllowedAccounts = []string{"missing", "a"}
	plugin = testRouterPlugin(route)
	host.executeCalls = nil
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if codeFromError(err, "") != "model_route_unavailable" || len(host.executeCalls) != 0 {
		t.Fatalf("error = %v, calls = %#v", err, host.executeCalls)
	}
	host.catalogErr = errors.New("secret catalog failure")
	_, err = plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if codeFromError(err, "") != "model_route_unavailable" || strings.Contains(err.Error(), "secret") || len(host.executeCalls) != 0 {
		t.Fatalf("error = %v, calls = %#v", err, host.executeCalls)
	}
}

func TestRestrictedExecuteTerminalAndCooldown(t *testing.T) {
	route := testModelRoute("smart", routeStrategyPriority, 30, "model-a")
	route.AllowedAccounts = []string{"a", "b"}
	plugin := testRouterPlugin(route)
	host := routingTestHost()
	host.execute = func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: 400, Body: []byte("secret")}, nil
	}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if statusFromError(err) != 400 || strings.Contains(err.Error(), "secret") || len(host.executeCalls) != 1 {
		t.Fatalf("error = %v, calls = %#v", err, host.executeCalls)
	}
	host.execute = func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: 429}, nil
	}
	host.executeCalls = nil
	_, err = plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if codeFromError(err, "") != "model_route_unavailable" || len(host.executeCalls) != 2 {
		t.Fatalf("error = %v, calls = %#v", err, host.executeCalls)
	}
	_, err = plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if codeFromError(err, "") != "model_route_cooldown" || len(host.executeCalls) != 2 {
		t.Fatalf("error = %v, calls = %#v", err, host.executeCalls)
	}
}

func TestRestrictedExecuteCancellationStopsDespiteStatus(t *testing.T) {
	route := testModelRoute("smart", routeStrategyPriority, 30, "model-a", "model-b")
	route.AllowedAccounts = []string{"a", "b"}
	plugin := testRouterPlugin(route)
	host := routingTestHost()
	host.execute = func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: 429, Body: []byte("secret")}, context.DeadlineExceeded
	}
	_, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host)
	if !errors.Is(err, context.DeadlineExceeded) || len(host.executeCalls) != 1 {
		t.Fatalf("error = %v, calls = %#v", err, host.executeCalls)
	}
	host.execute = func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
		return pluginapi.HostModelExecutionResponse{StatusCode: 200}, nil
	}
	if _, err := plugin.executeWithHost(pluginapi.ExecutorRequest{Model: "smart"}, host); err != nil {
		t.Fatalf("terminal error cooled route: %v", err)
	}
}

func TestCountTokensIsExplicitlyUnsupported(t *testing.T) {
	plugin := testRouterPlugin(testModelRoute("smart", routeStrategyPriority, 30, "a"))
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

func jsonModel(t *testing.T, raw []byte) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode JSON: %v; body = %s", err, raw)
	}
	model, _ := body["model"].(string)
	return strings.TrimSpace(model)
}
