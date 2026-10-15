package main

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type failureClass string

const (
	failureCancelled             failureClass = "cancelled"
	failureDeadlineExhausted     failureClass = "deadline-exhausted"
	failureContextOverflow       failureClass = "context-overflow"
	failureUnsupportedCapability failureClass = "unsupported-capability"
	failureRequestTerminal       failureClass = "request-terminal"
	failureQuotaAuth             failureClass = "quota-auth-model-availability"
	failureAttemptTimeout        failureClass = "attempt-timeout"
	failureTransportTransient    failureClass = "transport-transient"
	failureServerTransient       failureClass = "server-transient"
	failureUnknown               failureClass = "unknown"
)

// attemptFailure is bounded, safe execution classification. It never retains request data.
type attemptFailure struct {
	class      failureClass
	status     int
	retry      bool
	penalize   bool
	terminal   bool
	retryAfter time.Duration
	err        error
}

func classifyAttemptFailure(err error, headers http.Header, policy fallbackPolicy, maxBackoff, remaining time.Duration) attemptFailure {
	if err == nil {
		return attemptFailure{}
	}
	status := statusFromError(err)
	if errors.Is(err, context.Canceled) {
		return attemptFailure{class: failureCancelled, status: status, terminal: true, err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return attemptFailure{class: failureDeadlineExhausted, status: status, terminal: true, err: err}
	}
	if terminalRequestError(err) {
		return attemptFailure{class: failureRequestTerminal, status: status, terminal: true, err: err}
	}
	code := strings.ToLower(strings.TrimSpace(codeFromError(err, "")))
	message := strings.ToLower(truncateFailureText(err.Error()))
	if structuredFailureCode(message, "context_length_exceeded", "context_window_exceeded", "max_context_length_exceeded", "context_overflow") || knownFailureCode(code, "context_length_exceeded", "context_window_exceeded", "max_context_length_exceeded", "context_overflow") {
		return attemptFailure{class: failureContextOverflow, status: status, retry: true, err: err}
	}
	if structuredFailureCode(message, "unsupported_capability", "unsupported_feature", "unsupported_model", "tool_use_not_supported", "reasoning_not_supported") || knownFailureCode(code, "unsupported_capability", "unsupported_feature", "unsupported_model", "tool_use_not_supported", "reasoning_not_supported") {
		return attemptFailure{class: failureUnsupportedCapability, status: status, retry: true, err: err}
	}
	if status > 0 {
		if _, excluded := policy.noFallback[status]; persistedResponseMiss(status, message) || excluded {
			return attemptFailure{class: failureRequestTerminal, status: status, terminal: true, err: err}
		}
		switch {
		case status == http.StatusRequestTimeout:
			return attemptFailure{class: failureAttemptTimeout, status: status, retry: true, penalize: true, retryAfter: boundedRetryAfter(headers, maxBackoff, remaining), err: err}
		case status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusTooManyRequests:
			return attemptFailure{class: failureQuotaAuth, status: status, retry: policy.shouldFallback(status), retryAfter: boundedRetryAfter(headers, maxBackoff, remaining), err: err}
		case status >= 500 && status <= 599:
			return attemptFailure{class: failureServerTransient, status: status, retry: policy.shouldFallback(status), penalize: policy.shouldFallback(status), retryAfter: boundedRetryAfter(headers, maxBackoff, remaining), err: err}
		default:
			if policy.shouldFallback(status) {
				return attemptFailure{class: failureServerTransient, status: status, retry: true, penalize: true, err: err}
			}
			return attemptFailure{class: failureRequestTerminal, status: status, terminal: true, err: err}
		}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return attemptFailure{class: failureAttemptTimeout, retry: true, penalize: true, err: err}
	}
	if recognizableTransientError(err) {
		return attemptFailure{class: failureTransportTransient, retry: true, penalize: true, err: err}
	}
	return attemptFailure{class: failureUnknown, terminal: true, err: err}
}

func knownFailureCode(code string, allowed ...string) bool {
	for _, item := range allowed {
		if code == item {
			return true
		}
	}
	return false
}

// structuredFailureCode accepts only JSON error codes, never free-text hints.
func structuredFailureCode(raw string, allowed ...string) bool {
	if len(raw) > 64<<10 {
		raw = raw[:64<<10]
	}
	var decoded any
	if json.Unmarshal([]byte(raw), &decoded) != nil {
		return false
	}
	return jsonContainsAllowedCode(decoded, 0, allowed)
}

func jsonContainsAllowedCode(value any, depth int, allowed []string) bool {
	if depth > 8 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if (key == "code" || key == "type") && isAllowedCode(item, allowed) {
				return true
			}
			if jsonContainsAllowedCode(item, depth+1, allowed) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if jsonContainsAllowedCode(item, depth+1, allowed) {
				return true
			}
		}
	}
	return false
}

func isAllowedCode(value any, allowed []string) bool {
	code, ok := value.(string)
	return ok && knownFailureCode(strings.ToLower(strings.TrimSpace(code)), allowed...)
}

func truncateFailureText(value string) string {
	if len(value) > 64<<10 {
		return value[:64<<10]
	}
	return value
}

func boundedRetryAfter(headers http.Header, maxBackoff, remaining time.Duration) time.Duration {
	if maxBackoff <= 0 || remaining <= 0 {
		return 0
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	var delay time.Duration
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		delay = time.Duration(seconds) * time.Second
	} else if when, err := http.ParseTime(raw); err == nil {
		delay = time.Until(when)
		if delay < 0 {
			delay = 0
		}
	}
	if delay > maxBackoff {
		delay = maxBackoff
	}
	if delay > remaining {
		delay = remaining
	}
	return delay
}

func backoffDelay(attempt int, resilience resilienceConfig, retryAfter, remaining time.Duration) time.Duration {
	if retryAfter > 0 && resilience.HonorRetryAfter {
		return retryAfter
	}
	if resilience.BaseBackoffMilliseconds <= 0 || resilience.MaxBackoffMilliseconds <= 0 || remaining <= 0 {
		return 0
	}
	max := time.Duration(resilience.MaxBackoffMilliseconds) * time.Millisecond
	delay := time.Duration(resilience.BaseBackoffMilliseconds) * time.Millisecond
	for range attempt {
		if delay >= max/2 {
			delay = max
			break
		}
		delay *= 2
	}
	if delay > max {
		delay = max
	}
	// Full jitter avoids synchronized retry bursts.
	delay = time.Duration(rand.Int64N(int64(delay) + 1))
	if delay > remaining {
		return 0
	}
	return delay
}

func waitForBackoff(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
