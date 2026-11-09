package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	routeStrategyPriority      = "priority"
	routeStrategyRoundRobin    = "round-robin"
	routeStrategyRandom        = "random"
	routeStrategyLastKnownGood = "last_known_good"
	defaultCooldownSeconds     = 60
	defaultRetentionDays       = 365
	maxRetentionDays           = 3650
	defaultTargetWeight        = 1
	maxTargetWeight            = 1_000_000
)

func defaultLastKnownGoodPolicy() lastKnownGoodPolicy {
	return lastKnownGoodPolicy{SuccessRateThreshold: 0.9, MinimumSamples: 20, ObservationWindowMinutes: 60}
}

// defaultFallbackOnStatus is the status set used when fallback_on_status is not
// configured, and it matches the 0.5.0 behaviour: target availability and quota
// errors (401, 402, 403, 404, 408, 429) and server-side faults (500, 502, 503,
// 504). Statuses that describe the rejected request rather than the target stay
// out of the set, so an invalid request does not cool a healthy route.
var defaultFallbackOnStatus = []int{
	401, 402, 403, 404, 408, 429, 500, 502, 503, 504,
}

type modelTarget struct {
	Model  string
	Weight int
}

type lastKnownGoodPolicy struct {
	SuccessRateThreshold     float64
	MinimumSamples           int
	ObservationWindowMinutes int
}

type modelRoute struct {
	Alias           string
	Strategy        string
	CooldownSeconds int
	LastKnownGood   lastKnownGoodPolicy
	Targets         []modelTarget
}

type modelTargetYAML struct {
	Model  string `yaml:"model" json:"model"`
	Weight *int   `yaml:"weight,omitempty" json:"weight,omitempty"`
}

type modelRouteYAML struct {
	Alias                    string             `yaml:"alias" json:"alias"`
	Strategy                 string             `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	CooldownSeconds          *int               `yaml:"cooldown_seconds,omitempty" json:"cooldown_seconds,omitempty"`
	LegacyCooldownSeconds    *int               `yaml:"cooldown-seconds,omitempty" json:"cooldown-seconds,omitempty"`
	SuccessRateThreshold     *float64           `yaml:"success_rate_threshold,omitempty" json:"success_rate_threshold,omitempty"`
	MinimumSamples           *int               `yaml:"minimum_samples,omitempty" json:"minimum_samples,omitempty"`
	ObservationWindowMinutes *int               `yaml:"observation_window_minutes,omitempty" json:"observation_window_minutes,omitempty"`
	Models                   *[]string          `yaml:"models,omitempty" json:"models,omitempty"`
	Targets                  *[]modelTargetYAML `yaml:"targets,omitempty" json:"targets,omitempty"`
}

type routerConfig struct {
	Enabled            bool
	DataPath           string
	RetentionDays      int
	Routes             []modelRoute
	FallbackOnStatus   []int
	FallbackDisabled   bool
	NoFallbackOnStatus []int
	RetryCodes         []string
	NoRetryCodes       []string
	MaxAttempts        int
}

// fallbackPolicy resolves the configured status lists into lookup sets. An
// omitted or empty fallback_on_status inherits defaultFallbackOnStatus, and
// no_fallback_on_status then removes statuses from whichever set applies.
func (c routerConfig) fallbackPolicy() fallbackPolicy {
	onStatus := c.FallbackOnStatus
	if len(onStatus) == 0 {
		onStatus = defaultFallbackOnStatus
	}
	return newFallbackPolicy(onStatus, c.NoFallbackOnStatus, c.RetryCodes, c.NoRetryCodes)
}

// attemptLimit caps how many targets one request may try. Disabled fallback
// allows one attempt; a positive max_attempts lowers the cap further.
func (c routerConfig) attemptLimit(targets int) int {
	if c.FallbackDisabled {
		return min(targets, 1)
	}
	if c.MaxAttempts > 0 {
		return min(targets, c.MaxAttempts)
	}
	return targets
}

type fallbackConfigYAML struct {
	Enabled                     *bool     `yaml:"enabled,omitempty" json:"enabled"`
	FallbackOnStatus            *[]int    `yaml:"fallback_on_status,omitempty" json:"fallback_on_status"`
	NoFallbackOnStatus          *[]int    `yaml:"no_fallback_on_status,omitempty" json:"no_fallback_on_status"`
	RetryCodes                  *[]string `yaml:"retry_codes,omitempty" json:"retry_codes"`
	NoRetryCodes                *[]string `yaml:"no_retry_codes,omitempty" json:"no_retry_codes"`
	MaxAttempts                 *int      `yaml:"max_attempts,omitempty" json:"max_attempts"`
	StreamFallbackBeforePayload *bool     `yaml:"stream_fallback_before_payload,omitempty" json:"stream_fallback_before_payload"`
}

const unsupportedAccountAwareSchemaError = "model-combo config does not support legacy account-aware schema fields account_pools or combos; migrate to routes with model targets"

type routerConfigYAML struct {
	Enabled       *bool               `yaml:"enabled,omitempty"`
	Priority      int                 `yaml:"priority,omitempty"`
	Store         yaml.Node           `yaml:"store,omitempty"`
	DataPath      string              `yaml:"data_path,omitempty"`
	RetentionDays *int                `yaml:"retention_days,omitempty"`
	Routes        *[]modelRouteYAML   `yaml:"routes,omitempty"`
	LegacyRoutes  *[]modelRouteYAML   `yaml:"model-routes,omitempty"`
	Fallback      *fallbackConfigYAML `yaml:"fallback,omitempty"`
}

func containsUnsupportedAccountAwareSchema(node *yaml.Node) bool {
	if node.Kind == yaml.MappingNode {
		for index := 0; index+1 < len(node.Content); index += 2 {
			if node.Content[index].Value == "account_pools" || node.Content[index].Value == "combos" {
				return true
			}
		}
	}
	for _, child := range node.Content {
		if containsUnsupportedAccountAwareSchema(child) {
			return true
		}
	}
	return false
}

func decodeRouterConfig(raw []byte) (routerConfig, error) {
	if len(bytes.TrimSpace(raw)) > 0 {
		var document yaml.Node
		if yaml.Unmarshal(raw, &document) == nil && containsUnsupportedAccountAwareSchema(&document) {
			return routerConfig{}, errors.New(unsupportedAccountAwareSchemaError)
		}
	}
	wire := routerConfigYAML{}
	if len(bytes.TrimSpace(raw)) > 0 {
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&wire); err != nil {
			return routerConfig{}, fmt.Errorf("decode %s config: %w", pluginID, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return routerConfig{}, fmt.Errorf("decode %s config: multiple YAML documents are not supported", pluginID)
			}
			return routerConfig{}, fmt.Errorf("decode %s config: %w", pluginID, err)
		}
	}
	if wire.Routes != nil && wire.LegacyRoutes != nil {
		return routerConfig{}, errors.New("model-combo config must not contain both routes and model-routes")
	}
	dataPath := strings.TrimSpace(wire.DataPath)
	if dataPath == "" {
		dataPath = defaultDataPathResolver()
	}
	absoluteDataPath, err := filepath.Abs(filepath.Clean(dataPath))
	if err != nil {
		return routerConfig{}, fmt.Errorf("resolve data_path: %w", err)
	}
	retentionDays := defaultRetentionDays
	if wire.RetentionDays != nil {
		retentionDays = *wire.RetentionDays
	}
	if retentionDays < 1 || retentionDays > maxRetentionDays {
		return routerConfig{}, fmt.Errorf("retention_days must be between 1 and %d", maxRetentionDays)
	}
	enabled := true
	if wire.Enabled != nil {
		enabled = *wire.Enabled
	}
	var routeWires []modelRouteYAML
	if wire.Routes != nil {
		routeWires = *wire.Routes
	} else if wire.LegacyRoutes != nil {
		routeWires = *wire.LegacyRoutes
	}
	routes := make([]modelRoute, 0, len(routeWires))
	aliases := make(map[string]int, len(routeWires))
	for index, item := range routeWires {
		if item.CooldownSeconds != nil && item.LegacyCooldownSeconds != nil {
			return routerConfig{}, fmt.Errorf("routes[%d] %q: must not contain both cooldown_seconds and cooldown-seconds", index, strings.TrimSpace(item.Alias))
		}
		cooldown := 0
		if item.CooldownSeconds != nil {
			cooldown = *item.CooldownSeconds
		} else if item.LegacyCooldownSeconds != nil {
			cooldown = *item.LegacyCooldownSeconds
		}
		policy := defaultLastKnownGoodPolicy()
		if item.SuccessRateThreshold != nil {
			policy.SuccessRateThreshold = *item.SuccessRateThreshold
		}
		if item.MinimumSamples != nil {
			policy.MinimumSamples = *item.MinimumSamples
		}
		if item.ObservationWindowMinutes != nil {
			policy.ObservationWindowMinutes = *item.ObservationWindowMinutes
		}
		if policy.SuccessRateThreshold <= 0 || policy.SuccessRateThreshold > 1 || policy.MinimumSamples < 1 || policy.ObservationWindowMinutes < 1 {
			return routerConfig{}, fmt.Errorf("routes[%d] %q: invalid last_known_good policy", index, strings.TrimSpace(item.Alias))
		}
		if item.Models != nil && item.Targets != nil {
			return routerConfig{}, fmt.Errorf("routes[%d] %q: must not contain both models and targets", index, strings.TrimSpace(item.Alias))
		}
		targets := []modelTarget(nil)
		if item.Targets != nil {
			targets = normalizeTargetList(*item.Targets)
		} else if item.Models != nil {
			targets = normalizeLegacyTargetList(*item.Models)
		}
		route := modelRoute{
			Alias:           strings.TrimSpace(item.Alias),
			Strategy:        strings.ToLower(strings.TrimSpace(item.Strategy)),
			CooldownSeconds: cooldown,
			LastKnownGood:   policy,
			Targets:         targets,
		}
		if route.Strategy == "" {
			route.Strategy = routeStrategyPriority
		}
		if route.CooldownSeconds == 0 {
			route.CooldownSeconds = defaultCooldownSeconds
		}
		if err := validateRoute(route, index, aliases); err != nil {
			return routerConfig{}, err
		}
		aliases[routeKey(route.Alias)] = index
		routes = append(routes, route)
	}
	for routeIndex, route := range routes {
		for targetIndex, target := range route.Targets {
			base, _, _ := splitThinkingSuffix(target.Model)
			if aliasIndex, exists := aliases[routeKey(base)]; exists {
				return routerConfig{}, fmt.Errorf("routes[%d] %q targets[%d] %q: target must not reference route alias at index %d", routeIndex, route.Alias, targetIndex, target.Model, aliasIndex)
			}
		}
	}
	var fallbackOnStatus, noFallbackOnStatus []int
	retryCodes := []string{"model_not_supported", "model_not_found", "model_unavailable", "resource_not_found"}
	var noRetryCodes []string
	maxAttempts := 0
	fallbackDisabled := false
	if wire.Fallback != nil {
		fb := wire.Fallback
		if fb.Enabled != nil {
			fallbackDisabled = !*fb.Enabled
		}
		if fb.StreamFallbackBeforePayload != nil && !*fb.StreamFallbackBeforePayload {
			return routerConfig{}, errors.New("fallback.stream_fallback_before_payload must be true: a stream that already emitted a payload cannot be spliced onto another target")
		}
		if fb.FallbackOnStatus != nil {
			fallbackOnStatus = append([]int(nil), (*fb.FallbackOnStatus)...)
		}
		if fb.NoFallbackOnStatus != nil {
			noFallbackOnStatus = append([]int(nil), (*fb.NoFallbackOnStatus)...)
		}
		if fb.RetryCodes != nil {
			codes, err := normalizeErrorCodes("fallback.retry_codes", *fb.RetryCodes)
			if err != nil {
				return routerConfig{}, err
			}
			retryCodes = codes
		}
		if fb.NoRetryCodes != nil {
			codes, err := normalizeErrorCodes("fallback.no_retry_codes", *fb.NoRetryCodes)
			if err != nil {
				return routerConfig{}, err
			}
			noRetryCodes = codes
		}
		if fb.MaxAttempts != nil {
			if *fb.MaxAttempts < 0 {
				return routerConfig{}, errors.New("fallback.max_attempts must be >= 0")
			}
			maxAttempts = *fb.MaxAttempts
		}
	}
	// Only the lists the configuration supplies are validated. The default set is
	// expanded when the policy is built, so an exclusion may name a status it does
	// not list, and an omitted or empty fallback_on_status keeps the defaults.
	if err := validateFallbackStatuses(fallbackOnStatus, noFallbackOnStatus); err != nil {
		return routerConfig{}, err
	}
	if !enabled {
		routes = nil
	}
	return routerConfig{
		Enabled:            enabled,
		DataPath:           absoluteDataPath,
		RetentionDays:      retentionDays,
		Routes:             routes,
		FallbackOnStatus:   fallbackOnStatus,
		FallbackDisabled:   fallbackDisabled,
		NoFallbackOnStatus: noFallbackOnStatus,
		RetryCodes:         retryCodes,
		NoRetryCodes:       noRetryCodes,
		MaxAttempts:        maxAttempts,
	}, nil
}

func validateFallbackStatuses(fallbackOnStatus, noFallbackOnStatus []int) error {
	listed := make(map[int]struct{}, len(fallbackOnStatus))
	for _, status := range fallbackOnStatus {
		if status < 100 || status > 599 {
			return fmt.Errorf("fallback_on_status contains invalid HTTP status %d (must be 100-599)", status)
		}
		listed[status] = struct{}{}
	}
	for _, status := range noFallbackOnStatus {
		if status < 100 || status > 599 {
			return fmt.Errorf("no_fallback_on_status contains invalid HTTP status %d (must be 100-599)", status)
		}
		if _, duplicate := listed[status]; duplicate {
			return fmt.Errorf("HTTP status %d appears in both fallback_on_status and no_fallback_on_status", status)
		}
	}
	return nil
}

func validateRoute(route modelRoute, index int, aliases map[string]int) error {
	if route.Alias == "" {
		return fmt.Errorf("routes[%d]: alias is required", index)
	}
	if _, _, hasSuffix := splitThinkingSuffix(route.Alias); hasSuffix {
		return fmt.Errorf("routes[%d] %q: alias must not include a thinking suffix", index, route.Alias)
	}
	if previous, duplicate := aliases[routeKey(route.Alias)]; duplicate {
		return fmt.Errorf("routes[%d] %q: duplicate alias (also at index %d)", index, route.Alias, previous)
	}
	if route.Strategy != routeStrategyPriority && route.Strategy != routeStrategyRoundRobin && route.Strategy != routeStrategyRandom && route.Strategy != routeStrategyLastKnownGood {
		return fmt.Errorf("routes[%d] %q: strategy must be %q, %q, %q, or %q", index, route.Alias, routeStrategyPriority, routeStrategyRoundRobin, routeStrategyRandom, routeStrategyLastKnownGood)
	}
	if route.CooldownSeconds < 0 {
		return fmt.Errorf("routes[%d] %q: cooldown_seconds must be >= 0", index, route.Alias)
	}
	if len(route.Targets) == 0 {
		return fmt.Errorf("routes[%d] %q: at least one model is required", index, route.Alias)
	}
	seen := make(map[string]int, len(route.Targets))
	for targetIndex, target := range route.Targets {
		model := strings.TrimSpace(target.Model)
		if model == "" {
			return fmt.Errorf("routes[%d] %q targets[%d]: model is required", index, route.Alias, targetIndex)
		}
		if target.Weight < 1 || target.Weight > maxTargetWeight {
			return fmt.Errorf("routes[%d] %q targets[%d] %q: weight must be between 1 and %d", index, route.Alias, targetIndex, model, maxTargetWeight)
		}
		key := routeKey(model)
		if previous, duplicate := seen[key]; duplicate {
			return fmt.Errorf("routes[%d] %q targets[%d] %q: duplicate model (also at index %d)", index, route.Alias, targetIndex, model, previous)
		}
		seen[key] = targetIndex
	}
	return nil
}

// normalizeErrorCodes trims and dedupes error codes, rejecting anything that
// is not a plain identifier so codes can never carry bodies or credentials.
func normalizeErrorCodes(field string, codes []string) ([]string, error) {
	seen := make(map[string]bool, len(codes))
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		code = strings.ToLower(strings.TrimSpace(code))
		if code == "" || seen[code] {
			continue
		}
		for _, r := range code {
			if !(r == '_' || r == '-' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				return nil, fmt.Errorf("%s contains invalid error code %q", field, code)
			}
		}
		seen[code] = true
		out = append(out, code)
	}
	return out, nil
}

func normalizeModelList(models []string) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		if model = strings.TrimSpace(model); model != "" {
			out = append(out, model)
		}
	}
	return out
}

func normalizeLegacyTargetList(models []string) []modelTarget {
	normalized := normalizeModelList(models)
	targets := make([]modelTarget, 0, len(normalized))
	for _, model := range normalized {
		targets = append(targets, modelTarget{Model: model, Weight: defaultTargetWeight})
	}
	return targets
}

func normalizeTargetList(targets []modelTargetYAML) []modelTarget {
	out := make([]modelTarget, 0, len(targets))
	for _, target := range targets {
		weight := defaultTargetWeight
		if target.Weight != nil {
			weight = *target.Weight
		}
		out = append(out, modelTarget{Model: strings.TrimSpace(target.Model), Weight: weight})
	}
	return out
}

func routeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func routeSignature(route modelRoute) string {
	var builder strings.Builder
	builder.WriteString(routeKey(route.Alias))
	builder.WriteByte('\n')
	builder.WriteString(route.Strategy)
	builder.WriteByte('\n')
	builder.WriteString(strconv.Itoa(route.CooldownSeconds))
	for _, target := range route.Targets {
		builder.WriteByte('\n')
		builder.WriteString(strings.TrimSpace(target.Model))
		builder.WriteByte('\n')
		builder.WriteString(strconv.Itoa(target.Weight))
	}
	return builder.String()
}

func splitThinkingSuffix(model string) (string, string, bool) {
	model = strings.TrimSpace(model)
	open := strings.LastIndex(model, "(")
	if open <= 0 || !strings.HasSuffix(model, ")") {
		return model, "", false
	}
	base := strings.TrimSpace(model[:open])
	suffix := strings.TrimSpace(model[open+1 : len(model)-1])
	if base == "" || suffix == "" {
		return model, "", false
	}
	return base, suffix, true
}

func targetModel(requested, target string) string {
	target = strings.TrimSpace(target)
	if _, _, hasSuffix := splitThinkingSuffix(target); hasSuffix {
		return target
	}
	_, suffix, hasSuffix := splitThinkingSuffix(requested)
	if !hasSuffix {
		return target
	}
	return fmt.Sprintf("%s(%s)", target, suffix)
}
