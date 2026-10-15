package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestClassifyAttemptFailure(t *testing.T) {
	policy := fallbackPolicy{onStatus: map[int]struct{}{http.StatusTooManyRequests: {}}, noFallback: map[int]struct{}{http.StatusBadRequest: {}}}
	cases := []struct {
		name     string
		err      error
		headers  http.Header
		class    failureClass
		retry    bool
		penalize bool
	}{
		{name: "server error", err: statusError{status: http.StatusServiceUnavailable}, class: failureServerTransient, retry: true, penalize: true},
		{name: "quota", err: statusError{status: http.StatusTooManyRequests}, class: failureQuotaAuth, retry: true, penalize: false},
		{name: "cancelled request", err: context.Canceled, class: failureCancelled},
		{name: "expired request", err: context.DeadlineExceeded, class: failureDeadlineExhausted},
		{name: "terminal client request", err: statusError{status: http.StatusBadRequest}, class: failureRequestTerminal},
		{name: "context overflow code", err: statusError{status: http.StatusBadRequest, message: `{"error":{"code":"context_length_exceeded"}}`}, class: failureContextOverflow, retry: true},
		{name: "unsupported capability code", err: statusError{status: http.StatusBadRequest, message: `{"error":{"code":"unsupported_capability"}}`}, class: failureUnsupportedCapability, retry: true},
		{name: "network timeout", err: timeoutFailure{}, class: failureAttemptTimeout, retry: true, penalize: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAttemptFailure(tc.err, tc.headers, policy, time.Second, time.Second)
			if got.class != tc.class || got.retry != tc.retry || got.penalize != tc.penalize {
				t.Fatalf("classifyAttemptFailure() = class %q retry %t penalize %t, want class %q retry %t penalize %t", got.class, got.retry, got.penalize, tc.class, tc.retry, tc.penalize)
			}
		})
	}
}

type timeoutFailure struct{}

func (timeoutFailure) Error() string   { return "timeout" }
func (timeoutFailure) Timeout() bool   { return true }
func (timeoutFailure) Temporary() bool { return true }

func TestRetryAfterAndBackoffBounds(t *testing.T) {
	headers := http.Header{"Retry-After": []string{"60"}}
	if got := boundedRetryAfter(headers, time.Second, 500*time.Millisecond); got != 500*time.Millisecond {
		t.Fatalf("boundedRetryAfter() = %v, want 500ms", got)
	}
	config := resilienceConfig{BaseBackoffMilliseconds: 100, MaxBackoffMilliseconds: 200}
	for attempt := range 10 {
		got := backoffDelay(attempt, config, 0, time.Second)
		if got < 0 || got > 200*time.Millisecond {
			t.Fatalf("backoffDelay(%d) = %v, want within [0, 200ms]", attempt, got)
		}
	}
	if got := classifyAttemptFailure(errors.New("opaque upstream failure"), nil, fallbackPolicy{}, time.Second, time.Second); !got.terminal {
		t.Fatal("opaque error must be terminal")
	}
}
