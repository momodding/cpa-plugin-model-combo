package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// StartStream may report the upstream status alongside the error. A 400 carried
// that way must move the request to the next target when the configuration lists
// it, rather than be treated as an ambiguous failure.
func TestExecuteStreamFailsOverWhenStartReturnsStatusWithError(t *testing.T) {
	plugin := testComboPluginFromConfig(t, `
fallback:
  fallback_on_status: [400]
routes:
  - alias: smart
    cooldown_seconds: 30
    targets:
      - model: provider-a
      - model: provider-b
`)
	host := &fakeModelHost{reads: map[string][]pluginapi.HostModelStreamReadResponse{
		"stream-b": {
			{Payload: []byte(`data: {"model":"provider-b"}` + "\n\n"), Done: true},
		},
	}}
	host.start = func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		if request.Model == "provider-a" {
			return pluginapi.HostModelStreamResponse{StatusCode: 400}, errors.New(`{"error":{"message":"reasoning settings conflict","code":"convert_request_failed"}}`)
		}
		return pluginapi.HostModelStreamResponse{StatusCode: 200, StreamID: "stream-b"}, nil
	}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart", SourceFormat: "openai"}, "plugin-stream", host)
	if err != nil {
		t.Fatalf("executeStreamWithHost() error = %v", err)
	}
	if len(host.startCalls) != 2 {
		t.Fatalf("a 400 reported with the error must fail over, start calls = %d", len(host.startCalls))
	}
}

func TestExecuteStreamHonorsCancelledContext(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a"))
	host := &fakeModelHost{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := plugin.executeStreamWithHost(ctx, pluginapi.ExecutorRequest{Model: "smart", SourceFormat: "openai"}, "plugin-stream", host)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeStreamWithHost(cancelled ctx) error = %v, want context.Canceled", err)
	}
	if len(host.startCalls) != 0 {
		t.Fatalf("executeStreamWithHost(cancelled ctx) start calls = %d, want 0", len(host.startCalls))
	}
}

func TestExecuteStreamRetriesAnotherPooledAccount(t *testing.T) {
	plugin := testComboPluginFromConfig(t, `
resilience: {max_attempts: 2, base_backoff_ms: 0, max_backoff_ms: 0}
account_pools:
  pool:
    accounts:
      - {key: first, auth_name: first, provider: openai}
      - {key: second, auth_name: second, provider: openai}
combos:
  - alias: smart
    groups:
      - name: primary
        targets: [{model: provider-a, account_pool: pool}]
`)
	host := &fakeModelHost{reads: map[string][]pluginapi.HostModelStreamReadResponse{
		"stream-second": {{Payload: []byte(`data: {"model":"provider-a"}` + "\n\n"), Done: true}},
	}}
	host.listAuth = func() ([]pluginapi.HostAuthFileEntry, error) {
		return []pluginapi.HostAuthFileEntry{{Name: "first", Provider: "openai", ID: "id-first"}, {Name: "second", Provider: "openai", ID: "id-second"}}, nil
	}
	host.start = func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		if request.AuthID == "id-first" {
			return pluginapi.HostModelStreamResponse{StatusCode: http.StatusServiceUnavailable}, errors.New("unavailable")
		}
		return pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-second"}, nil
	}
	if err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart", SourceFormat: "openai"}, "plugin-stream", host); err != nil {
		t.Fatalf("executeStreamWithHost() error = %v", err)
	}
	if len(host.startCalls) != 2 || host.startCalls[0].AuthID != "id-first" || host.startCalls[1].AuthID != "id-second" {
		t.Fatalf("account stream retries = %#v", host.startCalls)
	}
}

func TestExecuteStreamFailsClosedWhenAuthListFails(t *testing.T) {
	plugin := testComboPluginFromConfig(t, `
account_pools:
  pool:
    strategy: priority
    accounts:
      - {key: primary, auth_name: primary, provider: openai}
combos:
  - alias: smart
    groups:
      - name: primary
        targets: [{model: provider-a, account_pool: pool}]
`)
	host := &fakeModelHost{
		listAuth: func() ([]pluginapi.HostAuthFileEntry, error) {
			return nil, errors.New("auth list unavailable")
		},
	}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart"}, "plugin-stream", host)
	if statusFromError(err) != http.StatusServiceUnavailable || codeFromError(err, "") != "model_combo_unavailable" {
		t.Fatalf("executeStreamWithHost() error = %v, want unavailable route error", err)
	}
	if len(host.startCalls) != 0 {
		t.Fatalf("stream requests = %#v, want none", host.startCalls)
	}
}

func TestExecuteStreamReturnsCooldownWhenAllCandidatesCooling(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a"))
	plan := plugin.plans[0]
	group := plan.groups[0]
	candidate := group.candidates[0]
	plugin.comboRuntime.MarkFailure(plan, group, candidate, plan.resilience)
	host := &fakeModelHost{}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart"}, "plugin-stream", host)
	if statusFromError(err) != 429 || codeFromError(err, "") != "model_combo_cooldown" || len(host.startCalls) != 0 {
		t.Fatalf("executeStreamWithHost() error = %v, starts = %d", err, len(host.startCalls))
	}
}

func TestExecuteStreamDoesNotFailOverOnRequestError(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a", "provider-b"))
	rejected := &fakeModelHost{start: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		return pluginapi.HostModelStreamResponse{StatusCode: 400}, errors.New(`{"error":{"message":"invalid request"}}`)
	}}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart", SourceFormat: "openai"}, "plugin-stream", rejected)
	if statusFromError(err) != 400 || len(rejected.startCalls) != 1 {
		t.Fatalf("executeStreamWithHost() error = %v, start calls = %d", err, len(rejected.startCalls))
	}
	healthy := &fakeModelHost{reads: map[string][]pluginapi.HostModelStreamReadResponse{
		"stream-a": {
			{Payload: []byte(`data: {"model":"provider-a"}` + "\n\n"), Done: true},
		},
	}}
	healthy.start = func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		return pluginapi.HostModelStreamResponse{StatusCode: 200, StreamID: "stream-a"}, nil
	}
	if err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart", SourceFormat: "openai"}, "plugin-stream", healthy); err != nil {
		t.Fatalf("a valid stream after a rejected one must still reach a provider: %v", err)
	}
	if len(healthy.startCalls) != 1 {
		t.Fatalf("valid stream calls = %d, want 1", len(healthy.startCalls))
	}
	if model := healthy.startCalls[0].Model; model != "provider-a" {
		t.Fatalf("the rejected stream must not cool the preferred target, valid stream selected %q", model)
	}
}

func TestExecuteStreamDoesNotFailOverOnExcludedStatusFromError(t *testing.T) {
	plugin := testComboPluginFromConfig(t, `
fallback:
  fallback_on_status: [400]
  no_fallback_on_status: [429]
routes:
  - alias: smart
    targets:
      - model: provider-a
      - model: provider-b
`)
	host := &fakeModelHost{start: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		return pluginapi.HostModelStreamResponse{}, statusError{status: 429, message: "rate limit exceeded"}
	}}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart"}, "plugin-stream", host)
	if statusFromError(err) != 429 || len(host.startCalls) != 1 {
		t.Fatalf("executeStreamWithHost() error = %v, start calls = %d, want status 429 and one call", err, len(host.startCalls))
	}
}

func TestExecuteStreamFailsOverBeforePayload(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a", "provider-b"))
	host := &fakeModelHost{reads: map[string][]pluginapi.HostModelStreamReadResponse{
		"stream-b": {
			{Payload: []byte(`data: {"model":"provider-b",`), Done: false},
			{Payload: []byte(`"choices":[]}` + "\n\n"), Done: true},
		},
	}}
	host.start = func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		if request.Model == "provider-a" {
			return pluginapi.HostModelStreamResponse{StatusCode: 429}, nil
		}
		return pluginapi.HostModelStreamResponse{StatusCode: 200, StreamID: "stream-b"}, nil
	}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart", SourceFormat: "openai"}, "plugin-stream", host)
	if err != nil {
		t.Fatalf("executeStreamWithHost() error = %v", err)
	}
	if len(host.startCalls) != 2 || len(host.emitted) != 1 {
		t.Fatalf("start calls = %d, emitted = %d", len(host.startCalls), len(host.emitted))
	}
	if payload := string(host.emitted[0]); !strings.Contains(payload, `"model":"smart"`) || strings.Contains(payload, "provider-b") {
		t.Fatalf("emitted payload = %q", payload)
	}
}

func TestExecuteStreamFailsOverFromEmptyStreamBeforePayload(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a", "provider-b"))
	host := &fakeModelHost{reads: map[string][]pluginapi.HostModelStreamReadResponse{
		"stream-a": {{Done: true}},
		"stream-b": {{Payload: []byte(`data: {"model":"provider-b"}` + "\n\n"), Done: true}},
	}}
	host.start = func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		if request.Model == "provider-a" {
			return pluginapi.HostModelStreamResponse{StatusCode: 200, StreamID: "stream-a"}, nil
		}
		return pluginapi.HostModelStreamResponse{StatusCode: 200, StreamID: "stream-b"}, nil
	}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart", SourceFormat: "openai"}, "plugin-stream", host)
	if err != nil {
		t.Fatalf("executeStreamWithHost() error = %v", err)
	}
	if len(host.startCalls) != 2 || len(host.emitted) != 1 {
		t.Fatalf("start calls = %d, emitted = %d", len(host.startCalls), len(host.emitted))
	}
}

func TestExecuteStreamDoesNotRetryAfterPayload(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a", "provider-b"))
	host := &fakeModelHost{reads: map[string][]pluginapi.HostModelStreamReadResponse{
		"stream-a": {
			{Payload: []byte(`data: {"model":"provider-a"}` + "\n\n")},
			{Error: "status 429: late rate limit", Done: true},
		},
	}}
	host.start = func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		return pluginapi.HostModelStreamResponse{StatusCode: 200, StreamID: "stream-a"}, nil
	}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "smart"}, "plugin-stream", host)
	if statusFromError(err) != 429 || len(host.startCalls) != 1 || len(host.emitted) != 1 {
		t.Fatalf("error = %v, start calls = %d, emitted = %d", err, len(host.startCalls), len(host.emitted))
	}
}

func TestExecuteStreamAttemptsWeightedTargetOncePerRequest(t *testing.T) {
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
	host := &fakeModelHost{}
	host.start = func(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		return pluginapi.HostModelStreamResponse{StatusCode: 429}, statusError{status: 429, message: request.Model + " unavailable"}
	}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{Model: "weighted"}, "plugin-stream", host)
	if statusFromError(err) != 503 || codeFromError(err, "") != "model_combo_unavailable" {
		t.Fatalf("executeStreamWithHost() error = %v", err)
	}
	if len(host.startCalls) != 2 || host.startCalls[0].Model != "a" || host.startCalls[1].Model != "b" {
		t.Fatalf("weighted stream calls = %#v, want a then b once", host.startCalls)
	}
}

func TestStreamModelRewriterBuffersSplitSSE(t *testing.T) {
	rewriter := newStreamModelRewriter("smart")
	if first := rewriter.Rewrite([]byte(`data: {"model":"physical",`)); first != nil {
		t.Fatalf("first Rewrite() = %q", first)
	}
	second := rewriter.Rewrite([]byte(`"choices":[]}` + "\n\n"))
	if payload := string(second); !strings.Contains(payload, `"model":"smart"`) || strings.Contains(payload, "physical") {
		t.Fatalf("second Rewrite() = %q", payload)
	}
	if tail := rewriter.Finish(); tail != nil {
		t.Fatalf("Finish() = %q", tail)
	}
}

func TestStreamModelRewriterSeparatesResponsesEventAndDataChunks(t *testing.T) {
	rewriter := newStreamModelRewriter("responses-router-alias")
	if first := rewriter.Rewrite([]byte("event: response.created")); first != nil {
		t.Fatalf("first Rewrite() = %q", first)
	}
	second := rewriter.Rewrite([]byte(`data: {"type":"response.created","response":{"model":"physical-model"}}`))
	payload := string(second)
	if !strings.HasPrefix(payload, "event: response.created\ndata: ") {
		t.Fatalf("second Rewrite() did not preserve the SSE line boundary: %q", payload)
	}
	if !strings.Contains(payload, `"model":"responses-router-alias"`) || strings.Contains(payload, "physical-model") {
		t.Fatalf("second Rewrite() did not rewrite response.model: %q", payload)
	}
	if tail := rewriter.Finish(); tail != nil {
		t.Fatalf("Finish() = %q", tail)
	}
}

func TestStreamModelRewriterPreservesResponsesEventDataPairs(t *testing.T) {
	rewriter := newStreamModelRewriter("responses-router-alias")
	chunks := []string{
		"event: response.created",
		`data: {"type":"response.created","response":{"model":"physical-model"}}`,
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"OK"}`,
		"event: response.completed",
		`data: {"type":"response.completed","response":{"model":"physical-model"}}`,
	}
	var outputs []string
	for _, chunk := range chunks {
		if rewritten := rewriter.Rewrite([]byte(chunk)); len(rewritten) > 0 {
			outputs = append(outputs, string(rewritten))
		}
	}
	if tail := rewriter.Finish(); len(tail) > 0 {
		outputs = append(outputs, string(tail))
	}
	wantEvents := []string{"response.created", "response.output_text.delta", "response.completed"}
	if len(outputs) != len(wantEvents) {
		t.Fatalf("rewritten outputs = %#v, want %d event/data pairs", outputs, len(wantEvents))
	}
	for index, event := range wantEvents {
		if !strings.HasPrefix(outputs[index], "event: "+event+"\ndata: ") {
			t.Fatalf("rewritten pair %d = %q", index, outputs[index])
		}
	}
	combined := strings.Join(outputs, "\n\n")
	if !strings.Contains(combined, "response.completed") || strings.Count(combined, `"model":"responses-router-alias"`) != 2 || strings.Contains(combined, "physical-model") {
		t.Fatalf("rewritten stream = %q", combined)
	}
}

func TestStreamModelRewriterSeparatesGluedSSEEvents(t *testing.T) {
	rewriter := newStreamModelRewriter("smart")
	payload := rewriter.Rewrite([]byte(`data: {"model":"one"}data: {"model":"two"}`))
	if strings.Count(string(payload), `"model":"smart"`) != 2 || strings.Contains(string(payload), `}data:`) {
		t.Fatalf("Rewrite() = %q", payload)
	}
}

func TestExecuteStreamPreservesForwardingProtocol(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 30, "provider-a"))
	host := &fakeModelHost{reads: map[string][]pluginapi.HostModelStreamReadResponse{
		"stream-a": {{Payload: []byte("data: {\"model\":\"provider-a\"}\n\n"), Done: true}},
	}}
	host.start = func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		return pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-a"}, nil
	}
	err := plugin.executeStreamWithHost(context.Background(), pluginapi.ExecutorRequest{
		Model:           "smart",
		SourceFormat:    "openai",
		OriginalRequest: []byte(`{"model":"smart","messages":[]}`),
		Headers:         http.Header{"X-Trace": {"trace-id"}},
		Query:           url.Values{"alt": {"json"}},
		Alt:             "sse",
	}, "plugin-stream", host)
	if err != nil || len(host.startCalls) != 1 {
		t.Fatalf("executeStreamWithHost() error = %v, calls = %d", err, len(host.startCalls))
	}
	call := host.startCalls[0]
	if call.EntryProtocol != "openai" || call.ExitProtocol != "openai" || call.Model != "provider-a" || call.Headers.Get("X-Trace") != "trace-id" || call.Query.Get("alt") != "json" || call.Alt != "sse" || !strings.Contains(string(call.Body), `"model":"provider-a"`) {
		t.Fatalf("forwarded stream request = %#v", call)
	}
}

func TestForwardStreamAttemptWithAuthPinsExactID(t *testing.T) {
	host := &fakeModelHost{start: func(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
		return pluginapi.HostModelStreamResponse{StatusCode: http.StatusBadGateway}, nil
	}}
	_, _ = forwardStreamAttemptWithAuth(pluginapi.ExecutorRequest{}, executionBody{}, "provider/model", "requested", "stream", host, "opaque-auth-id")
	if len(host.startCalls) != 1 || host.startCalls[0].AuthID != "opaque-auth-id" {
		t.Fatalf("stream AuthID = %#v, want exact pin", host.startCalls)
	}
}
