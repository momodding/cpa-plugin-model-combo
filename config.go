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
	routeStrategyPriority           = "priority"
	routeStrategyRoundRobin         = "round-robin"
	routeStrategyWeightedRoundRobin = "weighted-round-robin"
	defaultCooldownSeconds          = 60
	defaultRetentionDays            = 365
	maxRetentionDays                = 3650
	defaultTargetWeight             = 1
	maxTargetWeight                 = 1_000_000
	defaultMaxAttempts              = 4
	maxCombos                       = 128
	maxAccountPools                 = 128
	maxAccountsPerPool              = 256
	maxGroupsPerCombo               = 16
	maxTargetsPerGroup              = 32
	maxTargetsPerCombo              = 256
	maxAttempts                     = 16
	maxConfigNameLength             = 300
)

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

type modelRoute struct {
	Alias           string
	Strategy        string
	CooldownSeconds int
	Targets         []modelTarget
}

type modelCapability string

const (
	modelCapabilityReasoning     modelCapability = "reasoning"
	modelCapabilityTools         modelCapability = "tools"
	modelCapabilityVisionInput   modelCapability = "vision-input"
	modelCapabilityAudioInput    modelCapability = "audio-input"
	modelCapabilityDocumentInput modelCapability = "document-input"
	modelCapabilityJSONSchema    modelCapability = "json-schema"
)

type capabilityPolicy string

const (
	capabilityPolicyCompatibleIfKnown capabilityPolicy = "compatible-if-known"
	capabilityPolicyRequireDeclared   capabilityPolicy = "require-declared"
)

type comboTarget struct {
	Model                string
	Weight               int
	Capabilities         []modelCapability
	CapabilitiesDeclared bool
	AccountPool          string
}

type accountPool struct {
	Name     string
	Strategy string
	Accounts []account
}

type account struct {
	Key      string
	AuthName string
	Provider string
	Priority int
	Weight   int
}

type modelGroup struct {
	Name            string
	Strategy        string
	CooldownSeconds int
	Targets         []comboTarget
}

type modelCombo struct {
	Alias            string
	Groups           []modelGroup
	CapabilityPolicy capabilityPolicy
	legacy           bool
}

type resilienceConfig struct {
	MaxAttempts             int
	DeadlineMilliseconds    int
	BaseBackoffMilliseconds int
	MaxBackoffMilliseconds  int
	HonorRetryAfter         bool
	CircuitFailureThreshold int
	CircuitOpenSeconds      int
	CircuitMaxOpenSeconds   int
}

type modelTargetYAML struct {
	Model  string `yaml:"model" json:"model"`
	Weight *int   `yaml:"weight,omitempty" json:"weight,omitempty"`
}

type comboTargetYAML struct {
	Model        string    `yaml:"model" json:"model"`
	Weight       *int      `yaml:"weight,omitempty" json:"weight,omitempty"`
	Capabilities *[]string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	AccountPool  string    `yaml:"account_pool,omitempty" json:"account_pool,omitempty"`
}

type modelGroupYAML struct {
	Name                  string             `yaml:"name" json:"name"`
	Strategy              string             `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	CooldownSeconds       *int               `yaml:"cooldown_seconds,omitempty" json:"cooldown_seconds,omitempty"`
	LegacyCooldownSeconds *int               `yaml:"cooldown-seconds,omitempty" json:"cooldown-seconds,omitempty"`
	Models                *[]string          `yaml:"models,omitempty" json:"models,omitempty"`
	Targets               *[]comboTargetYAML `yaml:"targets,omitempty" json:"targets,omitempty"`
}

type accountPoolYAML struct {
	Strategy string        `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Accounts []accountYAML `yaml:"accounts" json:"accounts"`
}

type accountYAML struct {
	Key      string `yaml:"key" json:"key"`
	AuthName string `yaml:"auth_name" json:"auth_name"`
	Provider string `yaml:"provider" json:"provider"`
	Priority int    `yaml:"priority,omitempty" json:"priority,omitempty"`
	Weight   *int   `yaml:"weight,omitempty" json:"weight,omitempty"`
}

// sessionAffinityYAML reserves per-combo affinity configuration.
// CLIProxyAPI v7.3.9 does not expose an authenticated caller session ID to ExecutorRequest.
// Headers, query, body, metadata, and AuthID cannot safely identify a caller session.
type sessionAffinityYAML struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

type modelComboYAML struct {
	Alias            string              `yaml:"alias" json:"alias"`
	Groups           *[]modelGroupYAML   `yaml:"groups" json:"groups"`
	CapabilityPolicy string              `yaml:"capability_policy,omitempty" json:"capability_policy,omitempty"`
	SessionAffinity  sessionAffinityYAML `yaml:"session_affinity,omitempty" json:"session_affinity,omitempty"`
}
type resilienceConfigYAML struct {
	MaxAttempts             *int  `yaml:"max_attempts,omitempty"`
	DeadlineMilliseconds    *int  `yaml:"deadline_ms,omitempty"`
	BaseBackoffMilliseconds *int  `yaml:"base_backoff_ms,omitempty"`
	MaxBackoffMilliseconds  *int  `yaml:"max_backoff_ms,omitempty"`
	HonorRetryAfter         *bool `yaml:"honor_retry_after,omitempty"`
	CircuitFailureThreshold *int  `yaml:"circuit_failure_threshold,omitempty"`
	CircuitOpenSeconds      *int  `yaml:"circuit_open_seconds,omitempty"`
	CircuitMaxOpenSeconds   *int  `yaml:"circuit_max_open_seconds,omitempty"`
}
type modelRouteYAML struct {
	Alias                 string             `yaml:"alias" json:"alias"`
	Strategy              string             `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	CooldownSeconds       *int               `yaml:"cooldown_seconds,omitempty" json:"cooldown_seconds,omitempty"`
	LegacyCooldownSeconds *int               `yaml:"cooldown-seconds,omitempty" json:"cooldown-seconds,omitempty"`
	Models                *[]string          `yaml:"models,omitempty" json:"models,omitempty"`
	Targets               *[]modelTargetYAML `yaml:"targets,omitempty" json:"targets,omitempty"`
}

type routerConfig struct {
	Enabled            bool
	DataPath           string
	RetentionDays      int
	Routes             []modelRoute
	Combos             []modelCombo
	AccountPools       map[string]accountPool
	Resilience         resilienceConfig
	FallbackOnStatus   []int
	NoFallbackOnStatus []int
}

// fallbackPolicy resolves the configured status lists into lookup sets. An
// omitted or empty fallback_on_status inherits defaultFallbackOnStatus, and
// no_fallback_on_status then removes statuses from whichever set applies.
func (c routerConfig) fallbackPolicy() fallbackPolicy {
	onStatus := c.FallbackOnStatus
	if len(onStatus) == 0 {
		onStatus = defaultFallbackOnStatus
	}
	return newFallbackPolicy(onStatus, c.NoFallbackOnStatus)
}

type fallbackConfigYAML struct {
	FallbackOnStatus                   *[]int `yaml:"fallback_on_status,omitempty"`
	NoFallbackOnStatus                 *[]int `yaml:"no_fallback_on_status,omitempty"`
	StreamFallbackBeforeFirstChunkOnly *bool  `yaml:"stream_fallback_before_first_chunk_only,omitempty"`
}
type routerConfigYAML struct {
	Enabled       *bool                      `yaml:"enabled,omitempty"`
	Priority      int                        `yaml:"priority,omitempty"`
	Store         yaml.Node                  `yaml:"store,omitempty"`
	DataPath      string                     `yaml:"data_path,omitempty"`
	RetentionDays *int                       `yaml:"retention_days,omitempty"`
	Combos        *[]modelComboYAML          `yaml:"combos,omitempty"`
	AccountPools  map[string]accountPoolYAML `yaml:"account_pools,omitempty"`
	Resilience    *resilienceConfigYAML      `yaml:"resilience,omitempty"`
	Routes        *[]modelRouteYAML          `yaml:"routes,omitempty"`
	LegacyRoutes  *[]modelRouteYAML          `yaml:"model-routes,omitempty"`
	Fallback      *fallbackConfigYAML        `yaml:"fallback,omitempty"`
}

func decodeRouterConfig(raw []byte) (routerConfig, error) {
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
	if len(routeWires) > maxCombos {
		return routerConfig{}, fmt.Errorf("routes must contain at most %d entries", maxCombos)
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
	combos := normalizeLegacyRoutes(routes)
	canonicalCombos, err := normalizeComboList(wire.Combos, len(combos), aliases)
	if err != nil {
		return routerConfig{}, err
	}
	accountPools, err := normalizeAccountPools(wire.AccountPools)
	if err != nil {
		return routerConfig{}, err
	}
	if len(combos)+len(canonicalCombos) > maxCombos {
		return routerConfig{}, fmt.Errorf("routes and combos together must contain at most %d entries", maxCombos)
	}
	combos = append(combos, canonicalCombos...)
	if err := validateComboAliasesAndTargets(combos); err != nil {
		return routerConfig{}, err
	}
	for _, combo := range canonicalCombos {
		routes = append(routes, flattenCombo(combo))
	}
	if err := validateComboAccountPools(canonicalCombos, accountPools); err != nil {
		return routerConfig{}, err
	}
	resilience, err := normalizeResilience(wire.Resilience)
	if err != nil {
		return routerConfig{}, err
	}
	var fallbackOnStatus []int
	var noFallbackOnStatus []int
	if wire.Fallback != nil {
		if wire.Fallback.StreamFallbackBeforeFirstChunkOnly != nil && !*wire.Fallback.StreamFallbackBeforeFirstChunkOnly {
			return routerConfig{}, errors.New("fallback.stream_fallback_before_first_chunk_only must be true: a stream that already emitted a payload cannot be spliced onto another target")
		}
		if wire.Fallback.FallbackOnStatus != nil {
			fallbackOnStatus = append([]int(nil), (*wire.Fallback.FallbackOnStatus)...)
		}
		if wire.Fallback.NoFallbackOnStatus != nil {
			noFallbackOnStatus = append([]int(nil), (*wire.Fallback.NoFallbackOnStatus)...)
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
		combos = nil
	}
	return routerConfig{
		Enabled:            enabled,
		DataPath:           absoluteDataPath,
		RetentionDays:      retentionDays,
		Routes:             routes,
		AccountPools:       accountPools,
		Combos:             combos,
		Resilience:         resilience,
		FallbackOnStatus:   fallbackOnStatus,
		NoFallbackOnStatus: noFallbackOnStatus,
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
	if route.Strategy != routeStrategyPriority && route.Strategy != routeStrategyRoundRobin {
		return fmt.Errorf("routes[%d] %q: strategy must be %q or %q", index, route.Alias, routeStrategyPriority, routeStrategyRoundRobin)
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

func normalizeLegacyRoutes(routes []modelRoute) []modelCombo {
	combos := make([]modelCombo, 0, len(routes))
	for _, route := range routes {
		strategy := route.Strategy
		if strategy == routeStrategyRoundRobin {
			for _, target := range route.Targets {
				if target.Weight != defaultTargetWeight {
					strategy = routeStrategyWeightedRoundRobin
					break
				}
			}
		}
		targets := make([]comboTarget, len(route.Targets))
		for index, target := range route.Targets {
			targets[index] = comboTarget{Model: target.Model, Weight: target.Weight}
		}
		combos = append(combos, modelCombo{Alias: route.Alias, Groups: []modelGroup{{Name: "default", Strategy: strategy, CooldownSeconds: route.CooldownSeconds, Targets: targets}}, legacy: true})
	}
	return combos
}

func normalizeComboList(raw *[]modelComboYAML, legacyCount int, aliases map[string]int) ([]modelCombo, error) {
	if raw == nil {
		return nil, nil
	}
	if len(*raw) > maxCombos {
		return nil, fmt.Errorf("combos must contain at most %d entries", maxCombos)
	}
	combos := make([]modelCombo, 0, len(*raw))
	for comboIndex, item := range *raw {
		alias := strings.TrimSpace(item.Alias)
		if alias == "" {
			return nil, fmt.Errorf("combos[%d]: alias is required", comboIndex)
		}
		if len(alias) > maxConfigNameLength {
			return nil, fmt.Errorf("combos[%d] %q: alias must be at most %d characters", comboIndex, alias, maxConfigNameLength)
		}
		if _, _, hasSuffix := splitThinkingSuffix(alias); hasSuffix {
			return nil, fmt.Errorf("combos[%d] %q: alias must not include a thinking suffix", comboIndex, alias)
		}
		if previous, duplicate := aliases[routeKey(alias)]; duplicate {
			return nil, fmt.Errorf("combos[%d] %q: duplicate alias (also at index %d)", comboIndex, alias, previous)
		}
		policy, err := normalizeCapabilityPolicy(item.CapabilityPolicy)
		if err != nil {
			return nil, fmt.Errorf("combos[%d] %q: %w", comboIndex, alias, err)
		}
		if item.SessionAffinity.Enabled {
			return nil, fmt.Errorf("combos[%d] %q: session_affinity.enabled requires a reliable caller session ID, but CPA v7.3.9 ExecutorRequest lacks one", comboIndex, alias)
		}
		if item.Groups == nil || len(*item.Groups) == 0 {
			return nil, fmt.Errorf("combos[%d] %q: at least one group is required", comboIndex, alias)
		}
		if len(*item.Groups) > maxGroupsPerCombo {
			return nil, fmt.Errorf("combos[%d] %q: groups must contain at most %d entries", comboIndex, alias, maxGroupsPerCombo)
		}
		groups := make([]modelGroup, 0, len(*item.Groups))
		groupNames := make(map[string]int, len(*item.Groups))
		targetCount := 0
		for groupIndex, group := range *item.Groups {
			normalized, err := normalizeGroup(group, comboIndex, alias, groupIndex, groupNames)
			if err != nil {
				return nil, err
			}
			targetCount += len(normalized.Targets)
			if targetCount > maxTargetsPerCombo {
				return nil, fmt.Errorf("combos[%d] %q: must contain at most %d targets", comboIndex, alias, maxTargetsPerCombo)
			}
			groups = append(groups, normalized)
		}
		aliases[routeKey(alias)] = legacyCount + comboIndex
		combos = append(combos, modelCombo{Alias: alias, Groups: groups, CapabilityPolicy: policy})
	}
	return combos, nil
}

func normalizeGroup(raw modelGroupYAML, comboIndex int, alias string, groupIndex int, names map[string]int) (modelGroup, error) {
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		return modelGroup{}, fmt.Errorf("combos[%d] %q groups[%d]: name is required", comboIndex, alias, groupIndex)
	}
	if len(name) > maxConfigNameLength {
		return modelGroup{}, fmt.Errorf("combos[%d] %q groups[%d] %q: name must be at most %d characters", comboIndex, alias, groupIndex, name, maxConfigNameLength)
	}
	if previous, duplicate := names[routeKey(name)]; duplicate {
		return modelGroup{}, fmt.Errorf("combos[%d] %q groups[%d] %q: duplicate name (also at index %d)", comboIndex, alias, groupIndex, name, previous)
	}
	if raw.CooldownSeconds != nil && raw.LegacyCooldownSeconds != nil {
		return modelGroup{}, fmt.Errorf("combos[%d] %q groups[%d] %q: must not contain both cooldown_seconds and cooldown-seconds", comboIndex, alias, groupIndex, name)
	}
	if raw.Models != nil && raw.Targets != nil {
		return modelGroup{}, fmt.Errorf("combos[%d] %q groups[%d] %q: must contain exactly one of models or targets", comboIndex, alias, groupIndex, name)
	}
	if raw.Models == nil && raw.Targets == nil {
		return modelGroup{}, fmt.Errorf("combos[%d] %q groups[%d] %q: must contain exactly one of models or targets", comboIndex, alias, groupIndex, name)
	}
	strategy := strings.ToLower(strings.TrimSpace(raw.Strategy))
	if strategy == "" {
		strategy = routeStrategyPriority
	}
	if strategy != routeStrategyPriority && strategy != routeStrategyRoundRobin && strategy != routeStrategyWeightedRoundRobin {
		return modelGroup{}, fmt.Errorf("combos[%d] %q groups[%d] %q: unsupported strategy %q", comboIndex, alias, groupIndex, name, strategy)
	}
	cooldown := defaultCooldownSeconds
	if raw.CooldownSeconds != nil {
		cooldown = *raw.CooldownSeconds
	} else if raw.LegacyCooldownSeconds != nil {
		cooldown = *raw.LegacyCooldownSeconds
	}
	if cooldown < 0 {
		return modelGroup{}, fmt.Errorf("combos[%d] %q groups[%d] %q: cooldown_seconds must be >= 0", comboIndex, alias, groupIndex, name)
	}
	targets, err := normalizeComboTargets(raw, comboIndex, alias, groupIndex, name)
	if err != nil {
		return modelGroup{}, err
	}
	names[routeKey(name)] = groupIndex
	return modelGroup{Name: name, Strategy: strategy, CooldownSeconds: cooldown, Targets: targets}, nil
}

func normalizeComboTargets(raw modelGroupYAML, comboIndex int, alias string, groupIndex int, name string) ([]comboTarget, error) {
	var targets []comboTarget
	if raw.Models != nil {
		targets = make([]comboTarget, 0, len(*raw.Models))
		for _, model := range normalizeModelList(*raw.Models) {
			targets = append(targets, comboTarget{Model: model, Weight: defaultTargetWeight})
		}
	} else {
		targets = make([]comboTarget, 0, len(*raw.Targets))
		for _, target := range *raw.Targets {
			weight := defaultTargetWeight
			if target.Weight != nil {
				weight = *target.Weight
			}
			capabilities, err := normalizeCapabilities(target.Capabilities)
			if err != nil {
				return nil, fmt.Errorf("combos[%d] %q groups[%d] %q target %q: %w", comboIndex, alias, groupIndex, name, strings.TrimSpace(target.Model), err)
			}
			targets = append(targets, comboTarget{Model: strings.TrimSpace(target.Model), Weight: weight, Capabilities: capabilities, CapabilitiesDeclared: target.Capabilities != nil, AccountPool: strings.TrimSpace(target.AccountPool)})
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("combos[%d] %q groups[%d] %q: at least one target is required", comboIndex, alias, groupIndex, name)
	}
	if len(targets) > maxTargetsPerGroup {
		return nil, fmt.Errorf("combos[%d] %q groups[%d] %q: targets must contain at most %d entries", comboIndex, alias, groupIndex, name, maxTargetsPerGroup)
	}
	rawStrategy := strings.ToLower(strings.TrimSpace(raw.Strategy))
	if rawStrategy == "" {
		rawStrategy = routeStrategyPriority
	}
	seen := make(map[string]int, len(targets))
	for targetIndex, target := range targets {
		if target.Model == "" {
			return nil, fmt.Errorf("combos[%d] %q groups[%d] %q targets[%d]: model is required", comboIndex, alias, groupIndex, name, targetIndex)
		}
		if len(target.Model) > maxConfigNameLength {
			return nil, fmt.Errorf("combos[%d] %q groups[%d] %q targets[%d] %q: model must be at most %d characters", comboIndex, alias, groupIndex, name, targetIndex, target.Model, maxConfigNameLength)
		}
		if target.Weight < 1 || target.Weight > maxTargetWeight {
			return nil, fmt.Errorf("combos[%d] %q groups[%d] %q targets[%d] %q: weight must be between 1 and %d", comboIndex, alias, groupIndex, name, targetIndex, target.Model, maxTargetWeight)
		}
		if rawStrategy == routeStrategyPriority && target.Weight != defaultTargetWeight {
			return nil, fmt.Errorf("combos[%d] %q groups[%d] %q: priority strategy requires weight 1", comboIndex, alias, groupIndex, name)
		}
		if rawStrategy == routeStrategyRoundRobin && target.Weight != defaultTargetWeight {
			return nil, fmt.Errorf("combos[%d] %q groups[%d] %q: round-robin strategy requires weight 1", comboIndex, alias, groupIndex, name)
		}
		base, _, _ := splitThinkingSuffix(target.Model)
		key := routeKey(base)
		if previous, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("combos[%d] %q groups[%d] %q targets[%d] %q: duplicate model (also at index %d)", comboIndex, alias, groupIndex, name, targetIndex, target.Model, previous)
		}
		seen[key] = targetIndex
	}
	return targets, nil
}

func normalizeCapabilities(raw *[]string) ([]modelCapability, error) {
	if raw == nil {
		return nil, nil
	}
	capabilities := make([]modelCapability, 0, len(*raw))
	seen := make(map[modelCapability]struct{}, len(*raw))
	for _, value := range *raw {
		capability := modelCapability(strings.ToLower(strings.TrimSpace(value)))
		switch capability {
		case modelCapabilityReasoning, modelCapabilityTools, modelCapabilityVisionInput, modelCapabilityAudioInput, modelCapabilityDocumentInput, modelCapabilityJSONSchema:
		default:
			return nil, fmt.Errorf("unknown capability %q", value)
		}
		if _, duplicate := seen[capability]; duplicate {
			return nil, fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	return capabilities, nil
}

func normalizeCapabilityPolicy(raw string) (capabilityPolicy, error) {
	policy := capabilityPolicy(strings.ToLower(strings.TrimSpace(raw)))
	if policy == "" {
		return capabilityPolicyCompatibleIfKnown, nil
	}
	if policy != capabilityPolicyCompatibleIfKnown && policy != capabilityPolicyRequireDeclared {
		return "", fmt.Errorf("capability_policy must be %q or %q", capabilityPolicyCompatibleIfKnown, capabilityPolicyRequireDeclared)
	}
	return policy, nil
}

func validateComboAliasesAndTargets(combos []modelCombo) error {
	aliases := make(map[string]int, len(combos))
	for comboIndex, combo := range combos {
		aliases[routeKey(combo.Alias)] = comboIndex
	}
	for comboIndex, combo := range combos {
		seen := make(map[string]struct{})
		for groupIndex, group := range combo.Groups {
			for targetIndex, target := range group.Targets {
				base, _, _ := splitThinkingSuffix(target.Model)
				key := routeKey(base)
				if _, recursive := aliases[key]; recursive {
					return fmt.Errorf("combos[%d] %q groups[%d] %q targets[%d] %q: target must not reference combo alias", comboIndex, combo.Alias, groupIndex, group.Name, targetIndex, target.Model)
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("combos[%d] %q groups[%d] %q targets[%d] %q: duplicate candidate in combo", comboIndex, combo.Alias, groupIndex, group.Name, targetIndex, target.Model)
				}
				seen[key] = struct{}{}
			}
		}
	}
	return nil
}

func flattenCombo(combo modelCombo) modelRoute {
	targets := make([]modelTarget, 0)
	for _, group := range combo.Groups {
		for _, target := range group.Targets {
			targets = append(targets, modelTarget{Model: target.Model, Weight: target.Weight})
		}
	}
	cooldown := defaultCooldownSeconds
	strategy := routeStrategyPriority
	if len(combo.Groups) > 0 {
		cooldown = combo.Groups[0].CooldownSeconds
		strategy = combo.Groups[0].Strategy
		if strategy == routeStrategyWeightedRoundRobin {
			strategy = routeStrategyRoundRobin
		}
	}
	return modelRoute{Alias: combo.Alias, Strategy: strategy, CooldownSeconds: cooldown, Targets: targets}
}

func normalizeResilience(raw *resilienceConfigYAML) (resilienceConfig, error) {
	config := resilienceConfig{MaxAttempts: defaultMaxAttempts, DeadlineMilliseconds: 120000, BaseBackoffMilliseconds: 100, MaxBackoffMilliseconds: 2000, HonorRetryAfter: true, CircuitFailureThreshold: 3, CircuitOpenSeconds: 60, CircuitMaxOpenSeconds: 900}
	if raw == nil {
		return config, nil
	}
	if raw.MaxAttempts != nil {
		config.MaxAttempts = *raw.MaxAttempts
	}
	if raw.DeadlineMilliseconds != nil {
		config.DeadlineMilliseconds = *raw.DeadlineMilliseconds
	}
	if raw.BaseBackoffMilliseconds != nil {
		config.BaseBackoffMilliseconds = *raw.BaseBackoffMilliseconds
	}
	if raw.MaxBackoffMilliseconds != nil {
		config.MaxBackoffMilliseconds = *raw.MaxBackoffMilliseconds
	}
	if raw.HonorRetryAfter != nil {
		config.HonorRetryAfter = *raw.HonorRetryAfter
	}
	if raw.CircuitFailureThreshold != nil {
		config.CircuitFailureThreshold = *raw.CircuitFailureThreshold
	}
	if raw.CircuitOpenSeconds != nil {
		config.CircuitOpenSeconds = *raw.CircuitOpenSeconds
	}
	if raw.CircuitMaxOpenSeconds != nil {
		config.CircuitMaxOpenSeconds = *raw.CircuitMaxOpenSeconds
	}
	if config.MaxAttempts < 1 || config.MaxAttempts > maxAttempts {
		return resilienceConfig{}, fmt.Errorf("resilience.max_attempts must be between 1 and %d", maxAttempts)
	}
	if config.DeadlineMilliseconds < 1 {
		return resilienceConfig{}, errors.New("resilience.deadline_ms must be > 0")
	}
	if config.BaseBackoffMilliseconds < 0 || config.MaxBackoffMilliseconds < config.BaseBackoffMilliseconds {
		return resilienceConfig{}, errors.New("resilience backoff bounds are invalid")
	}
	if config.CircuitFailureThreshold < 1 || config.CircuitOpenSeconds < 1 || config.CircuitMaxOpenSeconds < config.CircuitOpenSeconds {
		return resilienceConfig{}, errors.New("resilience circuit bounds are invalid")
	}
	return config, nil
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
