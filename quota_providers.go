package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const quotaClaudeURL = "https://api.anthropic.com/api/oauth/usage"
const quotaCodexURL = "https://chatgpt.com/backend-api/wham/usage"

var quotaAntigravityURLs = []string{"https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary", "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"}

type quotaMetadata struct{ project, account string }

func quotaAllowedEndpoint(endpoint string) bool {
	switch endpoint {
	case quotaClaudeURL, quotaCodexURL, "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary", "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary":
		return true
	}
	return false
}

func quotaManagementURL() (string, string) {
	raw := strings.TrimSpace(os.Getenv("MODEL_COMBO_MANAGEMENT_URL"))
	if raw == "" {
		raw = "http://127.0.0.1:8317"
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", "invalid_management_url"
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
	default:
		return "", "invalid_management_url"
	}
	return strings.TrimRight(u.String(), "/"), ""
}
func fetchQuota(ctx context.Context, c accountCredential, metadata quotaMetadata) quotaSnapshot {
	fail := func(reason string) quotaSnapshot { return quotaSnapshot{State: quotaUnknown, Err: reason} }
	base, reason := quotaManagementURL()
	if reason != "" {
		return fail(reason)
	}
	key := os.Getenv("MODEL_COMBO_MANAGEMENT_KEY")
	if key == "" {
		return fail("management_key_missing")
	}
	provider := strings.ToLower(c.Provider)
	var endpoints []string
	method := "GET"
	data := ""
	headers := map[string]string{"Authorization": "Bearer $TOKEN$", "Content-Type": "application/json"}
	switch provider {
	case "claude":
		endpoints = []string{quotaClaudeURL}
		headers["User-Agent"] = "claude-cli/2.1.280 (external, cli)"
		headers["anthropic-beta"] = "oauth-2025-04-20"
	case "codex":
		endpoints = []string{quotaCodexURL}
		headers["User-Agent"] = "codex-tui/0.149.1"
		if metadata.account != "" {
			headers["Chatgpt-Account-Id"] = metadata.account
		}
	case "antigravity":
		if metadata.project == "" {
			return fail("missing_project_id")
		}
		endpoints = quotaAntigravityURLs
		method = "POST"
		body, _ := json.Marshal(map[string]string{"project": metadata.project})
		data = string(body)
		headers["User-Agent"] = "antigravity/hub/2.23.0 darwin/arm64"
	default:
		return fail("unsupported_provider")
	}
	if c.AuthIndex == "" {
		return fail("missing_auth_index")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	last := fail("upstream_unavailable")
	for _, endpoint := range endpoints {
		if !quotaAllowedEndpoint(endpoint) {
			return fail("invalid_upstream_url")
		}
		payload, _ := json.Marshal(map[string]any{"auth_index": c.AuthIndex, "method": method, "url": endpoint, "header": headers, "data": data})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v0/management/api-call", bytes.NewReader(payload))
		if err != nil {
			return fail("management_request_failed")
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			last = fail("management_request_failed")
			continue
		}
		var wrapped struct {
			StatusCode int    `json:"status_code"`
			Body       string `json:"body"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&wrapped)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			last = fail("management_request_failed")
			continue
		}
		if err != nil {
			last = fail("invalid_response")
			continue
		}
		if wrapped.StatusCode < 200 || wrapped.StatusCode >= 300 {
			if wrapped.StatusCode == 429 || wrapped.StatusCode >= 500 {
				last = fail("upstream_unavailable")
			} else {
				last = fail("upstream_rejected")
			}
			continue
		}
		snapshot := parseProviderQuota(provider, []byte(wrapped.Body))
		snapshot.Source = provider
		if snapshot.State != quotaUnknown {
			return snapshot
		}
		last = snapshot
	}
	return last
}
func parseProviderQuota(provider string, data []byte) quotaSnapshot {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return quotaSnapshot{State: quotaUnknown, Err: "invalid_response"}
	}
	s := quotaSnapshot{State: quotaKnown}
	invalid := false
	add := func(name, scope string, raw json.RawMessage, field string) {
		if len(raw) == 0 || string(raw) == "null" {
			return
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			invalid = true
			return
		}
		var value float64
		if json.Unmarshal(obj[field], &value) != nil || math.IsNaN(value) || value < 0 || value > 100 {
			invalid = true
			return
		}
		w := quotaWindow{Name: name, Scope: scope, Remaining: 1 - value/100}
		var reset string
		if json.Unmarshal(obj["resets_at"], &reset) == nil {
			w.ResetAt, _ = time.Parse(time.RFC3339, reset)
		}
		var unix int64
		if json.Unmarshal(obj["reset_at"], &unix) == nil && unix > 0 {
			w.ResetAt = time.Unix(unix, 0).UTC()
		}
		s.Windows = append(s.Windows, w)
	}
	switch provider {
	case "claude":
		for _, x := range []struct{ name, scope string }{{"five_hour", ""}, {"seven_day", ""}, {"seven_day_opus", "opus"}, {"seven_day_sonnet", "sonnet"}} {
			add(x.name, x.scope, root[x.name], "utilization")
		}
	case "codex":
		var limits map[string]json.RawMessage
		if json.Unmarshal(root["rate_limit"], &limits) == nil {
			add("primary_window", "", limits["primary_window"], "used_percent")
			add("secondary_window", "", limits["secondary_window"], "used_percent")
		}
	case "antigravity":
		var groups []struct {
			Buckets []map[string]json.RawMessage `json:"buckets"`
		}
		if json.Unmarshal(root["groups"], &groups) != nil {
			break
		}
		for _, group := range groups {
			for _, bucket := range group.Buckets {
				var remaining float64
				raw := bucket["remainingFraction"]
				if len(raw) == 0 {
					raw = bucket["remaining_fraction"]
				}
				if json.Unmarshal(raw, &remaining) != nil || remaining < 0 || remaining > 1 || math.IsNaN(remaining) {
					invalid = true
					continue
				}
				var id, window, reset string
				json.Unmarshal(bucket["bucketId"], &id)
				if id == "" {
					json.Unmarshal(bucket["bucket_id"], &id)
				}
				json.Unmarshal(bucket["window"], &window)
				json.Unmarshal(bucket["resetTime"], &reset)
				if reset == "" {
					json.Unmarshal(bucket["reset_time"], &reset)
				}
				scope := ""
				for _, family := range []string{"gemini-", "claude-", "gpt-"} {
					if strings.HasPrefix(strings.ToLower(id), family) {
						scope = strings.TrimSuffix(family, "-")
						break
					}
				}
				if scope == "" {
					invalid = true
					continue
				}
				t, _ := time.Parse(time.RFC3339, reset)
				s.Windows = append(s.Windows, quotaWindow{Name: window, Scope: scope, Remaining: remaining, ResetAt: t})
			}
		}
	}
	if len(s.Windows) == 0 {
		return quotaSnapshot{State: quotaUnknown, Err: "invalid_response"}
	}
	if invalid {
		s.State = quotaPartial
	}
	return s
}
