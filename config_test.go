package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDecodeRouterConfigCanonicalAndLegacyKeys(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "canonical",
			raw: `routes:
  - alias: smart
    strategy: round-robin
    cooldown_seconds: 15
    targets:
      - model: provider-a
        weight: 3
      - model: provider-b
`,
		},
		{
			name: "legacy",
			raw: `model-routes:
  - alias: smart
    strategy: round-robin
    cooldown-seconds: 15
    models: [provider-a, provider-b]
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := decodeRouterConfig([]byte(test.raw))
			if err != nil {
				t.Fatalf("decodeRouterConfig() error = %v", err)
			}
			if !config.Enabled || len(config.Routes) != 1 {
				t.Fatalf("decodeRouterConfig() = %#v", config)
			}
			route := config.Routes[0]
			if route.Alias != "smart" || route.Strategy != routeStrategyRoundRobin || route.CooldownSeconds != 15 || len(route.Targets) != 2 {
				t.Fatalf("route = %#v", route)
			}
			wantTargets := []modelTarget{{Model: "provider-a", Weight: 1}, {Model: "provider-b", Weight: 1}}
			if test.name == "canonical" {
				wantTargets[0].Weight = 3
			}
			if !slices.Equal(route.Targets, wantTargets) {
				t.Fatalf("targets = %#v", route.Targets)
			}
		})
	}
}

func TestDecodeRouterConfigDefaults(t *testing.T) {
	previousResolver := defaultDataPathResolver
	wantPath := filepath.Join(t.TempDir(), "default.db")
	defaultDataPathResolver = func() string { return wantPath }
	t.Cleanup(func() { defaultDataPathResolver = previousResolver })
	config, err := decodeRouterConfig([]byte(`routes:
  - alias: smart
    models: [provider-a]
`))
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	route := config.Routes[0]
	if route.Strategy != routeStrategyPriority || route.CooldownSeconds != defaultCooldownSeconds {
		t.Fatalf("route defaults = %#v", route)
	}
	if config.DataPath != wantPath || config.RetentionDays != defaultRetentionDays {
		t.Fatalf("storage defaults = %#v", config)
	}
}

func TestDecodeRouterConfigStorageOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "usage.db")
	config, err := decodeRouterConfig([]byte("data_path: " + filepath.ToSlash(path) + "\nretention_days: 45\nroutes: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if config.DataPath != path || config.RetentionDays != 45 {
		t.Fatalf("storage config = %#v", config)
	}
	for _, days := range []int{0, maxRetentionDays + 1} {
		if _, err := decodeRouterConfig([]byte("retention_days: " + itoa(days) + "\nroutes: []\n")); err == nil || !strings.Contains(err.Error(), "retention_days") {
			t.Fatalf("retention_days=%d error = %v", days, err)
		}
	}
}

func TestDecodeRouterConfigDisabledClearsRoutes(t *testing.T) {
	config, err := decodeRouterConfig([]byte(`enabled: false
routes:
  - alias: smart
    models: [provider-a]
`))
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	if config.Enabled || config.Routes != nil {
		t.Fatalf("disabled config = %#v", config)
	}
}

func TestDecodeRouterConfigRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		message string
	}{
		{
			name: "both route keys",
			raw: `routes: []
model-routes: []
`,
			message: "both routes and model-routes",
		},
		{
			name: "both cooldown keys",
			raw: `routes:
  - alias: smart
    cooldown_seconds: 10
    cooldown-seconds: 20
    models: [provider-a]
`,
			message: "both cooldown_seconds and cooldown-seconds",
		},
		{
			name: "unknown field",
			raw: `routes: []
surprise: true
`,
			message: "field surprise not found",
		},
		{
			name: "duplicate alias",
			raw: `routes:
  - alias: Smart
    models: [provider-a]
  - alias: smart
    models: [provider-b]
`,
			message: "duplicate alias",
		},
		{
			name: "alias suffix",
			raw: `routes:
  - alias: smart(high)
    models: [provider-a]
`,
			message: "alias must not include a thinking suffix",
		},
		{
			name: "nested alias",
			raw: `routes:
  - alias: smart
    models: [backup]
  - alias: backup
    models: [provider-b]
`,
			message: "target must not reference route alias",
		},
		{
			name: "negative cooldown",
			raw: `routes:
  - alias: smart
    cooldown_seconds: -1
    models: [provider-a]
`,
			message: "cooldown_seconds must be >= 0",
		},
		{
			name: "both model schemas",
			raw: `routes:
  - alias: smart
    models: [provider-a]
    targets:
      - model: provider-b
`,
			message: "both models and targets",
		},
		{
			name: "zero weight",
			raw: `routes:
  - alias: smart
    targets:
      - model: provider-a
        weight: 0
`,
			message: "weight must be between",
		},
		{
			name: "weight too large",
			raw: `routes:
  - alias: smart
    targets:
      - model: provider-a
        weight: 1000001
`,
			message: "weight must be between",
		},
		{
			name: "empty canonical model",
			raw: `routes:
  - alias: smart
    targets:
      - model: ""
`,
			message: "model is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeRouterConfig([]byte(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("decodeRouterConfig() error = %v, want substring %q", err, test.message)
			}
		})
	}
}

func TestRouterConfigFallbackPolicyDefaultsWhenEmpty(t *testing.T) {
	policy := routerConfig{}.fallbackPolicy()
	for _, status := range defaultFallbackOnStatus {
		if !policy.shouldFallback(status) {
			t.Fatalf("default policy must fall back on %d", status)
		}
	}
}

func TestDecodeRouterConfigFallback(t *testing.T) {
	raw := []byte(`
fallback:
  fallback_on_status: [400, 429]
  no_fallback_on_status: []
  stream_fallback_before_first_chunk_only: true
routes:
  - alias: demo
    targets:
      - model: target-a
`)
	cfg, err := decodeRouterConfig(raw)
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	policy := cfg.fallbackPolicy()
	if !policy.shouldFallback(400) || !policy.shouldFallback(429) {
		t.Fatal("configured statuses must fall back")
	}
	if policy.shouldFallback(404) {
		t.Fatal("404 is not in the configured list and must not fall back")
	}
	if !policy.shouldFallback(503) {
		t.Fatal("503 must still fall back through the 5xx rule")
	}
}

func TestDecodeRouterConfigRejectsInvalidFallback(t *testing.T) {
	cases := map[string]string{
		"overlap": `
fallback:
  fallback_on_status: [429]
  no_fallback_on_status: [429]
routes:
  - alias: demo
    targets:
      - model: target-a
`,
		"out of range": `
fallback:
  fallback_on_status: [99]
routes:
  - alias: demo
    targets:
      - model: target-a
`,
		"stream splice": `
fallback:
  stream_fallback_before_first_chunk_only: false
routes:
  - alias: demo
    targets:
      - model: target-a
`,
		"unknown key": `
fallback:
  fallback_on_500: true
routes:
  - alias: demo
    targets:
      - model: target-a
`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRouterConfig([]byte(raw)); err == nil {
				t.Fatalf("decodeRouterConfig() accepted invalid fallback config for %q", name)
			}
		})
	}
}

func TestDecodeRouterConfigExclusionOverridesDefaults(t *testing.T) {
	raw := []byte(`
fallback:
  no_fallback_on_status: [400, 429, 503]
routes:
  - alias: demo
    targets:
      - model: target-a
`)
	cfg, err := decodeRouterConfig(raw)
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	policy := cfg.fallbackPolicy()
	for _, status := range []int{400, 429, 503} {
		if policy.shouldFallback(status) {
			t.Fatalf("excluded status %d must not fall back", status)
		}
	}
	for _, status := range []int{402, 404, 504} {
		if !policy.shouldFallback(status) {
			t.Fatalf("status %d is not excluded and must keep the default", status)
		}
	}
}

func TestDecodeRouterConfigFallbackListFormsAgree(t *testing.T) {
	routes := `
routes:
  - alias: demo
    targets:
      - model: target-a
`
	cases := map[string]string{
		"omitted":        "fallback:\n  no_fallback_on_status: [400, 503]\n" + routes,
		"empty explicit": "fallback:\n  fallback_on_status: []\n  no_fallback_on_status: [400, 503]\n" + routes,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := decodeRouterConfig([]byte(raw))
			if err != nil {
				t.Fatalf("decodeRouterConfig() error = %v", err)
			}
			policy := cfg.fallbackPolicy()
			if policy.shouldFallback(400) || policy.shouldFallback(503) {
				t.Fatal("an exclusion must remove a status from the inherited default set")
			}
			if !policy.shouldFallback(404) || !policy.shouldFallback(500) {
				t.Fatal("an omitted or empty fallback_on_status must keep the default set")
			}
		})
	}
}

func TestDecodeRouterConfigNormalizesMixedCombos(t *testing.T) {
	cfg, err := decodeRouterConfig([]byte(`
routes:
  - alias: legacy
    strategy: round-robin
    cooldown_seconds: 15
    targets:
      - model: legacy-a
        weight: 2
      - model: legacy-b
        weight: 1
combos:
  - alias: modern
    groups:
      - name: primary
        strategy: weighted-round-robin
        cooldown_seconds: 20
        targets:
          - model: modern-a
            weight: 3
            capabilities: [reasoning, tools]
          - model: modern-b
            weight: 1
      - name: fallback
        models: [modern-c]
`))
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	if len(cfg.Combos) != 2 {
		t.Fatalf("combo count = %d, want 2", len(cfg.Combos))
	}
	legacy := cfg.Combos[0]
	if legacy.Alias != "legacy" || len(legacy.Groups) != 1 || legacy.Groups[0].Name != "default" || legacy.Groups[0].Strategy != routeStrategyWeightedRoundRobin {
		t.Fatalf("legacy combo = %#v", legacy)
	}
	modern := cfg.Combos[1]
	if modern.Alias != "modern" || len(modern.Groups) != 2 || modern.Groups[0].CooldownSeconds != 20 {
		t.Fatalf("modern combo = %#v", modern)
	}
	if got := modern.Groups[0].Targets[0]; got.Model != "modern-a" || got.Weight != 3 || !slices.Equal(got.Capabilities, []modelCapability{modelCapabilityReasoning, modelCapabilityTools}) {
		t.Fatalf("modern target = %#v", got)
	}
	if len(cfg.Routes) != 2 || cfg.Routes[0].Alias != "legacy" || cfg.Routes[1].Alias != "modern" {
		t.Fatalf("legacy route view = %#v", cfg.Routes)
	}
	if len(cfg.Routes[1].Targets) != 3 {
		t.Fatalf("flattened modern targets = %#v", cfg.Routes[1].Targets)
	}
}

func TestCompileLegacyComboCapsAttemptBudgetToTargets(t *testing.T) {
	combo := modelCombo{
		Alias: "legacy",
		Groups: []modelGroup{{
			Name:     "default",
			Strategy: routeStrategyRoundRobin,
			Targets:  []comboTarget{{Model: "legacy-a", Weight: 1}, {Model: "legacy-b", Weight: 1}},
		}},
		legacy: true,
	}
	plan := compileCombo(combo, resilienceConfig{MaxAttempts: 5})
	if plan.maxAttempts != 2 {
		t.Fatalf("legacy combo maxAttempts = %d, want 2", plan.maxAttempts)
	}
}

func TestDecodeRouterConfigRejectsInvalidCombos(t *testing.T) {
	cases := map[string]string{
		"missing alias": `combos: [{groups: [{name: primary, models: [model-a]}]}]`,
		"duplicate alias": `
routes: [{alias: duplicate, models: [model-a]}]
combos: [{alias: DUPLICATE, groups: [{name: primary, models: [model-b]}]}]
`,
		"duplicate group":           `combos: [{alias: demo, groups: [{name: primary, models: [model-a]}, {name: PRIMARY, models: [model-b]}]}]`,
		"unknown strategy":          `combos: [{alias: demo, groups: [{name: primary, strategy: adaptive, models: [model-a]}]}]`,
		"both target forms":         `combos: [{alias: demo, groups: [{name: primary, models: [model-a], targets: [{model: model-b}]}]}]`,
		"no target form":            `combos: [{alias: demo, groups: [{name: primary}]}]`,
		"conflicting cooldown":      `combos: [{alias: demo, groups: [{name: primary, cooldown_seconds: 1, cooldown-seconds: 2, models: [model-a]}]}]`,
		"round robin weight":        `combos: [{alias: demo, groups: [{name: primary, strategy: round-robin, targets: [{model: model-a, weight: 2}]}]}]`,
		"unknown capability":        `combos: [{alias: demo, groups: [{name: primary, targets: [{model: model-a, capabilities: [telepathy]}]}]}]`,
		"invalid capability policy": `combos: [{alias: demo, capability_policy: permissive, groups: [{name: primary, models: [model-a]}]}]`,
		"recursive candidate":       `combos: [{alias: demo, groups: [{name: primary, models: [demo]}]}]`,
		"resilience bound":          `resilience: {max_attempts: 17}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRouterConfig([]byte(raw)); err == nil {
				t.Fatalf("decodeRouterConfig() accepted invalid combo config")
			}
		})
	}
}

func TestDecodeRouterConfigValidatesAccountPoolStrategy(t *testing.T) {
	cases := map[string]string{
		"invalid strategy":          `account_pools: {primary: {strategy: adaptive, accounts: [{key: k, auth_name: a, provider: p, priority: 0}]}}`,
		"underscore strategy":       `account_pools: {primary: {strategy: weighted_round_robin, accounts: [{key: k, auth_name: a, provider: p, priority: 0}]}}`,
		"zero weight weighted":      `account_pools: {primary: {strategy: weighted-round-robin, accounts: [{key: k, auth_name: a, provider: p, priority: 0, weight: 0}]}}`,
		"weight too large weighted": `account_pools: {primary: {strategy: weighted-round-robin, accounts: [{key: k, auth_name: a, provider: p, priority: 0, weight: 1000001}]}}`,
		"nonone weight priority":    `account_pools: {primary: {strategy: priority, accounts: [{key: k, auth_name: a, provider: p, priority: 0, weight: 2}]}}`,
		"empty accounts":            `account_pools: {primary: {strategy: priority, accounts: []}}`,
		"missing key":               `account_pools: {primary: {strategy: priority, accounts: [{auth_name: a, provider: p, priority: 0}]}}`,
		"missing auth_name":         `account_pools: {primary: {strategy: priority, accounts: [{key: k, provider: p, priority: 0}]}}`,
		"missing provider":          `account_pools: {primary: {strategy: priority, accounts: [{key: k, auth_name: a, priority: 0}]}}`,
		"negative priority":         `account_pools: {primary: {strategy: priority, accounts: [{key: k, auth_name: a, provider: p, priority: -1}]}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRouterConfig([]byte(raw)); err == nil {
				t.Fatalf("decodeRouterConfig() accepted invalid account pool config: %s", name)
			}
		})
	}
}

func TestDecodeRouterConfigRejectsAccountPoolIdentifiersAndRoutePolicy(t *testing.T) {
	cases := map[string]string{
		"auth_id in account":           `account_pools: {primary: {accounts: [{key: k, auth_name: a, provider: p, auth_id: x}]}}`,
		"auth_index in account":        `account_pools: {primary: {accounts: [{key: k, auth_name: a, provider: p, auth_index: x}]}}`,
		"account_pool on route":        `routes: [{alias: smart, account_pool: primary, models: [model-a]}]`,
		"account_pool on route target": `routes: [{alias: smart, targets: [{model: model-a, account_pool: primary}]}]`,
		"account_pool on model-routes": `model-routes: [{alias: smart, targets: [{model: model-a, account_pool: primary}]}]`,
		"unknown combo account_pool":   `account_pools: {primary: {accounts: [{key: k, auth_name: a, provider: p}]}}` + "\n" + `combos: [{alias: demo, groups: [{name: primary, targets: [{model: model-a, account_pool: missing}]}]}]`,
		"duplicate pool ignoring case": `account_pools: {Primary: {accounts: [{key: k, auth_name: a, provider: p}]}, primary: {accounts: [{key: k2, auth_name: b, provider: p}]}}`,
		"duplicate key ignoring case":  `account_pools: {primary: {accounts: [{key: K, auth_name: a, provider: p}, {key: k, auth_name: b, provider: p}]}}`,
		"round-robin pool strategy":    `account_pools: {primary: {strategy: round-robin, accounts: [{key: k, auth_name: a, provider: p}]}}`,
		"priority above max":           `account_pools: {primary: {accounts: [{key: k, auth_name: a, provider: p, priority: 1000001}]}}`,
		"blank auth_name":              `account_pools: {primary: {accounts: [{key: k, auth_name: " ", provider: p}]}}`,
		"pool name too long":           `account_pools: {` + strings.Repeat("p", maxConfigNameLength+1) + `: {accounts: [{key: k, auth_name: a, provider: p}]}}`,
		"account key too long":         `account_pools: {primary: {accounts: [{key: ` + strings.Repeat("k", maxConfigNameLength+1) + `, auth_name: a, provider: p}]}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRouterConfig([]byte(raw)); err == nil {
				t.Fatalf("decodeRouterConfig() accepted invalid account pool config: %s", name)
			}
		})
	}
}

func accountPoolsYAML(pools, accountsPerPool int) string {
	var b strings.Builder
	b.WriteString("account_pools:\n")
	for p := range pools {
		fmt.Fprintf(&b, "  pool-%d: {accounts: [", p)
		for a := range accountsPerPool {
			if a > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "{key: k%d, auth_name: a%d, provider: p}", a, a)
		}
		b.WriteString("]}\n")
	}
	return b.String()
}

func TestDecodeRouterConfigBoundsAccountPools(t *testing.T) {
	if _, err := decodeRouterConfig([]byte(accountPoolsYAML(maxAccountPools, 1))); err != nil {
		t.Fatalf("decodeRouterConfig() at pool limit error = %v", err)
	}
	if _, err := decodeRouterConfig([]byte(accountPoolsYAML(1, maxAccountsPerPool))); err != nil {
		t.Fatalf("decodeRouterConfig() at account limit error = %v", err)
	}
	if _, err := decodeRouterConfig([]byte(accountPoolsYAML(maxAccountPools+1, 1))); err == nil {
		t.Fatal("decodeRouterConfig() accepted too many account pools")
	}
	if _, err := decodeRouterConfig([]byte(accountPoolsYAML(1, maxAccountsPerPool+1))); err == nil {
		t.Fatal("decodeRouterConfig() accepted too many accounts in one pool")
	}
	name := strings.Repeat("p", maxConfigNameLength)
	if _, err := decodeRouterConfig([]byte(`account_pools: {` + name + `: {accounts: [{key: k, auth_name: a, provider: p}]}}`)); err != nil {
		t.Fatalf("decodeRouterConfig() at name limit error = %v", err)
	}
}

func TestDecodeRouterConfigNormalizesAccountPoolReferences(t *testing.T) {
	cfg, err := decodeRouterConfig([]byte(`account_pools: {Primary: {strategy: weighted-round-robin, accounts: [{key: Main, auth_name: primary.json, provider: openai, weight: 3}, {key: backup, auth_name: backup.json, provider: openai}]}}
combos: [{alias: demo, groups: [{name: primary, targets: [{model: model-a, account_pool: " primary "}, {model: model-b}]}]}]`))
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	pool, ok := cfg.AccountPools["primary"]
	if !ok || pool.Name != "Primary" || pool.Strategy != routeStrategyWeightedRoundRobin || len(pool.Accounts) != 2 {
		t.Fatalf("AccountPools = %#v", cfg.AccountPools)
	}
	if pool.Accounts[0].Weight != 3 || pool.Accounts[1].Weight != defaultTargetWeight {
		t.Fatalf("account weights = %d, %d", pool.Accounts[0].Weight, pool.Accounts[1].Weight)
	}
	targets := cfg.Combos[0].Groups[0].Targets
	if targets[0].AccountPool != "primary" || targets[1].AccountPool != "" {
		t.Fatalf("target account pools = %q, %q", targets[0].AccountPool, targets[1].AccountPool)
	}
}

func TestDecodeRouterConfigSessionAffinityDisabledByDefault(t *testing.T) {
	config, err := decodeRouterConfig([]byte(`combos: [{alias: demo, groups: [{name: primary, models: [model-a]}]}]`))
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	if len(config.Combos) != 1 {
		t.Fatalf("combo count = %d", len(config.Combos))
	}
}

func TestDecodeRouterConfigRejectsEnabledSessionAffinity(t *testing.T) {
	_, err := decodeRouterConfig([]byte(`combos: [{alias: demo, session_affinity: {enabled: true}, groups: [{name: primary, models: [model-a]}]}]`))
	if err == nil {
		t.Fatal("decodeRouterConfig() accepted enabled session affinity")
	}
	const want = "session_affinity.enabled requires a reliable caller session ID, but CPA v7.3.9 ExecutorRequest lacks one"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("decodeRouterConfig() error = %q, want %q", err, want)
	}
}

func TestDecodeRouterConfigDefaultsCapabilityPolicy(t *testing.T) {
	config, err := decodeRouterConfig([]byte(`combos: [{alias: demo, groups: [{name: primary, models: [model-a]}]}]`))
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	if config.Combos[0].CapabilityPolicy != capabilityPolicyCompatibleIfKnown {
		t.Fatalf("CapabilityPolicy = %q", config.Combos[0].CapabilityPolicy)
	}
}
