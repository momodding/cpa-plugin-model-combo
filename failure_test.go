package main

import (
	"context"
	"errors"
	"testing"
)

const missingProviderErrorBody = `{"error":{"message":"unknown provider for model provider-a","type":"invalid_request_error","code":"model_not_found","param":"model"}}`

func TestEligibleRouteFailure(t *testing.T) {
	policy := newFallbackPolicy(defaultFallbackOnStatus, nil, nil, nil)
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "rate limit", err: statusError{status: 429, message: "rate limited"}, want: true},
		{name: "provider server error", err: statusError{status: 503, message: "unavailable"}, want: true},
		{name: "bad request", err: statusError{status: 400, message: "invalid prompt"}, want: false},
		{name: "missing provider", err: statusError{status: 400, message: missingProviderErrorBody}, want: true},
		{name: "prefixed missing provider", err: statusError{status: 400, message: "host model provider-a returned status 400: " + missingProviderErrorBody}, want: true},
		{name: "missing provider alternate message", err: statusError{status: 400, message: `{"error":{"code":"model_not_found","message":"no provider for model provider-a"}}`}, want: true},
		{name: "unstructured bad request", err: statusError{status: 400, message: "unknown provider for model provider-a"}, want: false},
		{name: "request mentions missing provider", err: statusError{status: 400, message: `{"error":{"code":"invalid_request_error","message":"invalid prompt: unknown provider for model provider-a"}}`}, want: false},
		{name: "different model error", err: statusError{status: 400, message: `{"error":{"code":"model_not_found","message":"invalid model parameter"}}`}, want: false},
		{name: "missing provider with different status", err: statusError{status: 422, message: missingProviderErrorBody}, want: false},
		{name: "unprocessable", err: statusError{status: 422, message: "invalid payload"}, want: false},
		{name: "conflict", err: statusError{status: 409, message: "conflict"}, want: false},
		{name: "payment required", err: statusError{status: 402, message: "package_quota_exhausted"}, want: true},
		{name: "persisted response miss", err: statusError{status: 404, message: "items are not persisted when store is false"}, want: false},
		{name: "store disabled response miss", err: statusError{status: 404, message: "store is false for this request"}, want: false},
		{name: "model missing", err: statusError{status: 404, message: "model not found"}, want: true},
		{name: "unlisted client error", err: statusError{status: 413, message: "payload too large"}, want: false},
		{name: "unlisted server error", err: statusError{status: 507, message: "insufficient storage"}, want: true},
		{name: "cloudflare timeout", err: statusError{status: 524, message: "a timeout occurred"}, want: true},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "recognized transport", err: errors.New("connection reset by peer"), want: true},
		{name: "tls handshake timeout", err: errors.New("net/http: TLS handshake timeout"), want: true},
		{name: "ambiguous error", err: errors.New("execution failed"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := eligibleRouteFailure(test.err, policy); got != test.want {
				t.Fatalf("eligibleRouteFailure(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestFallbackPolicyNoFallbackWins(t *testing.T) {
	policy := newFallbackPolicy([]int{400, 429}, []int{429}, nil, nil)
	if !policy.shouldFallback(400) {
		t.Fatal("400 should fall back when listed in fallback_on_status")
	}
	if policy.shouldFallback(429) {
		t.Fatal("no_fallback_on_status must win over fallback_on_status")
	}
	if policy.shouldFallback(401) {
		t.Fatal("an unlisted 4xx must not fall back")
	}
	if !policy.shouldFallback(500) {
		t.Fatal("every remaining 5xx must fall back")
	}
}

func TestEligibleRouteFailureUsesConfiguredStatuses(t *testing.T) {
	policy := newFallbackPolicy([]int{400}, []int{429}, nil, nil)
	if !eligibleRouteFailure(statusError{status: 400, message: "invalid prompt"}, policy) {
		t.Fatal("400 must fail over when fallback_on_status lists it")
	}
	if eligibleRouteFailure(statusError{status: 429, message: "rate limited"}, policy) {
		t.Fatal("429 must not fail over when no_fallback_on_status excludes it")
	}
	if eligibleRouteFailure(statusError{status: 404, message: "items are not persisted when store is false"}, policy) {
		t.Fatal("a persisted response miss describes the request, so it must never fail over")
	}
	if eligibleRouteFailure(statusError{status: 400, message: missingProviderErrorBody}, newFallbackPolicy(defaultFallbackOnStatus, []int{400}, nil, nil)) {
		t.Fatal("an explicit 400 exclusion must also exclude missing-provider errors")
	}
}

func TestEligibleRouteFailureStructuredCodes(t *testing.T) {
	policy := newFallbackPolicy(nil, nil, []string{"quota_exceeded", "invalid_prompt"}, []string{"invalid_prompt"})
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "allowlisted code", status: 400, body: `{"error":{"code":"quota_exceeded","message":"quota"}}`, want: true},
		{name: "allowlisted code with host prefix", status: 400, body: `model a returned status 400: {"error":{"code":"quota_exceeded"}}`, want: true},
		{name: "denylist overrides allowlist", status: 400, body: `{"error":{"code":"invalid_prompt"}}`, want: false},
		{name: "unlisted code", status: 400, body: `{"error":{"code":"other"}}`, want: false},
		{name: "allowlisted code on 422", status: 422, body: `{"error":{"code":"quota_exceeded"}}`, want: false},
		{name: "unstructured body", status: 400, body: "quota_exceeded", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eligibleRouteFailure(statusError{status: tt.status, message: tt.body}, policy); got != tt.want {
				t.Fatalf("eligibleRouteFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}
