package main

import (
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"strings"
	"time"
)

type accountPlan struct {
	name      string
	strategy  string
	accounts  []account
	signature string
}

func newAccountPlan(pool accountPool) *accountPlan {
	plan := &accountPlan{name: pool.Name, strategy: pool.Strategy, accounts: append([]account(nil), pool.Accounts...)}
	var b strings.Builder
	writeSignaturePart(&b, plan.name)
	writeSignaturePart(&b, plan.strategy)
	for _, account := range plan.accounts {
		writeSignaturePart(&b, account.Key)
		writeSignaturePart(&b, account.AuthName)
		writeSignaturePart(&b, account.Provider)
		fmt.Fprintf(&b, "%d\x00%d\x00", account.Priority, account.Weight)
	}
	plan.signature = b.String()
	return plan
}

// writeSignaturePart length-prefixes each config string so NUL bytes inside
// values cannot make distinct account plans share one signature.
func writeSignaturePart(b *strings.Builder, value string) {
	fmt.Fprintf(b, "%d:%s", len(value), value)
}

type resolvedAccount struct{ key, id string }

func resolveAccountPool(plan *accountPlan, entries []pluginapi.HostAuthFileEntry) []resolvedAccount {
	if plan == nil {
		return nil
	}
	resolved := make([]resolvedAccount, 0, len(plan.accounts))
	for _, configured := range plan.accounts {
		var match *pluginapi.HostAuthFileEntry
		for index := range entries {
			entry := &entries[index]
			if entry.Name != configured.AuthName || entry.Provider != configured.Provider {
				continue
			}
			if match != nil {
				match = nil
				break
			}
			match = entry
		}
		if match == nil || strings.TrimSpace(match.ID) == "" || match.Disabled || match.Unavailable || strings.EqualFold(match.Status, "disabled") || match.NextRetryAfter.After(time.Now()) {
			continue
		}
		resolved = append(resolved, resolvedAccount{key: configured.Key, id: match.ID})
	}
	return resolved
}

func excludeResolvedAccounts(resolved []resolvedAccount, attempted map[string]struct{}) []resolvedAccount {
	if len(attempted) == 0 {
		return resolved
	}
	available := make([]resolvedAccount, 0, len(resolved))
	for _, entry := range resolved {
		if _, attempted := attempted[entry.key+"\x00"+entry.id]; !attempted {
			available = append(available, entry)
		}
	}
	return available
}

// selectResolvedAccount restricts selection to the lowest available priority
// tier. Priority pools use declaration order; weighted pools use smooth WRR.
func selectResolvedAccount(runtime *comboRuntime, plan *accountPlan, resolved []resolvedAccount) (resolvedAccount, bool) {
	if plan == nil {
		return resolvedAccount{}, false
	}
	available := make(map[string]resolvedAccount, len(resolved))
	for _, entry := range resolved {
		available[entry.key] = entry
	}
	priority, found := 0, false
	for _, configured := range plan.accounts {
		if _, ok := available[configured.Key]; ok && (!found || configured.Priority < priority) {
			priority, found = configured.Priority, true
		}
	}
	if !found {
		return resolvedAccount{}, false
	}
	if plan.strategy == routeStrategyPriority {
		for _, configured := range plan.accounts {
			if configured.Priority == priority {
				if entry, ok := available[configured.Key]; ok {
					return entry, true
				}
			}
		}
	}
	if runtime == nil {
		return resolvedAccount{}, false
	}
	runtime.accountMu.Lock()
	defer runtime.accountMu.Unlock()
	current := runtime.accountCurrent[plan.signature]
	if current == nil {
		current = make(map[string]int)
		runtime.accountCurrent[plan.signature] = current
	}
	var selected account
	selectedWeight, totalWeight, selectedCurrent := 0, 0, 0
	for _, configured := range plan.accounts {
		if configured.Priority != priority {
			continue
		}
		if _, ok := available[configured.Key]; !ok {
			continue
		}
		totalWeight += configured.Weight
		current[configured.Key] += configured.Weight
		if selectedWeight == 0 || current[configured.Key] > selectedCurrent {
			selected, selectedWeight, selectedCurrent = configured, configured.Weight, current[configured.Key]
		}
	}
	if selectedWeight == 0 {
		return resolvedAccount{}, false
	}
	current[selected.Key] -= totalWeight
	return available[selected.Key], true
}

// MarkAccountFailure marks an account with cooldown after retryable failure.
// poolSig + logicalKey (account.key + "\x00" + account.id) must match exactly.
// retryAfter is clamped between minAccountCooldown and maxAccountCooldown.
func (runtime *comboRuntime) MarkAccountFailure(poolSig string, logicalKey string, retryAfter time.Duration) {
	if runtime == nil || poolSig == "" || logicalKey == "" {
		return
	}
	if retryAfter < minAccountCooldown {
		retryAfter = minAccountCooldown
	}
	if retryAfter > maxAccountCooldown {
		retryAfter = maxAccountCooldown
	}
	runtime.accountMu.Lock()
	defer runtime.accountMu.Unlock()
	cooldown := runtime.accountCooldown[poolSig]
	if cooldown == nil {
		cooldown = make(map[string]time.Time)
		runtime.accountCooldown[poolSig] = cooldown
	}
	cooldown[logicalKey] = runtime.now().Add(retryAfter)
}

// filterCooledAccounts returns resolved accounts excluding those with unexpired cooldown.
// Exact logical key (account.key + "\x00" + account.id) must match to be excluded.
// Deletes expired cooldown entries during filtering; deletes pool map if empty.
func filterCooledAccounts(runtime *comboRuntime, poolSig string, resolved []resolvedAccount) []resolvedAccount {
	if runtime == nil || poolSig == "" {
		return resolved
	}
	runtime.accountMu.Lock()
	defer runtime.accountMu.Unlock()
	cooldown := runtime.accountCooldown[poolSig]
	if cooldown == nil {
		return resolved
	}
	now := runtime.now()
	// Sweep all expired cooldown entries, regardless of resolved.
	for logicalKey, until := range cooldown {
		if !now.Before(until) {
			// Expired; delete it.
			delete(cooldown, logicalKey)
		}
	}
	// Delete pool map if empty after cleanup.
	if len(cooldown) == 0 {
		delete(runtime.accountCooldown, poolSig)
		return resolved
	}
	// If no accounts available, return early.
	if len(resolved) == 0 {
		return resolved
	}
	// Filter resolved for unexpired exact matches.
	filtered := make([]resolvedAccount, 0, len(resolved))
	for _, entry := range resolved {
		logicalKey := entry.key + "\x00" + entry.id
		until, exists := cooldown[logicalKey]
		if exists && now.Before(until) {
			// Unexpired cooldown; skip this account.
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func normalizeAccountPools(raw map[string]accountPoolYAML) (map[string]accountPool, error) {
	if len(raw) > maxAccountPools {
		return nil, fmt.Errorf("account_pools must contain at most %d entries", maxAccountPools)
	}
	out := make(map[string]accountPool, len(raw))
	for name, wire := range raw {
		name = strings.TrimSpace(name)
		key := routeKey(name)
		if name == "" || len(name) > maxConfigNameLength {
			return nil, fmt.Errorf("account_pools: pool name must be nonempty and at most %d characters", maxConfigNameLength)
		}
		if _, ok := out[key]; ok {
			return nil, fmt.Errorf("account_pools %q: duplicate pool name", name)
		}
		strategy := strings.ToLower(strings.TrimSpace(wire.Strategy))
		if strategy == "" {
			strategy = routeStrategyPriority
		}
		if strategy != routeStrategyPriority && strategy != routeStrategyWeightedRoundRobin {
			return nil, fmt.Errorf("account_pools %q: invalid strategy", name)
		}
		if len(wire.Accounts) == 0 || len(wire.Accounts) > maxAccountsPerPool {
			return nil, fmt.Errorf("account_pools %q: accounts must contain 1 to %d entries", name, maxAccountsPerPool)
		}
		pool := accountPool{Name: name, Strategy: strategy}
		seen := map[string]struct{}{}
		for i, item := range wire.Accounts {
			a := account{Key: strings.TrimSpace(item.Key), AuthName: strings.TrimSpace(item.AuthName), Provider: strings.TrimSpace(item.Provider), Priority: item.Priority, Weight: defaultTargetWeight}
			if item.Weight != nil {
				a.Weight = *item.Weight
			}
			if a.Key == "" || len(a.Key) > maxConfigNameLength || a.AuthName == "" || a.Provider == "" || a.Priority < 0 || a.Priority > maxTargetWeight {
				return nil, fmt.Errorf("account_pools %q accounts[%d]: invalid account", name, i)
			}
			if _, ok := seen[routeKey(a.Key)]; ok {
				return nil, fmt.Errorf("account_pools %q: duplicate account key %q", name, a.Key)
			}
			seen[routeKey(a.Key)] = struct{}{}
			if strategy == routeStrategyPriority && a.Weight != 1 {
				return nil, fmt.Errorf("account_pools %q: priority strategy requires weight 1", name)
			}
			if strategy == routeStrategyWeightedRoundRobin && (a.Weight < 1 || a.Weight > maxTargetWeight) {
				return nil, fmt.Errorf("account_pools %q accounts[%d]: invalid weight", name, i)
			}
			pool.Accounts = append(pool.Accounts, a)
		}
		out[key] = pool
	}
	return out, nil
}
func validateComboAccountPools(combos []modelCombo, pools map[string]accountPool) error {
	for ci, c := range combos {
		for gi, g := range c.Groups {
			for ti, t := range g.Targets {
				if t.AccountPool != "" {
					if _, ok := pools[routeKey(t.AccountPool)]; !ok {
						return fmt.Errorf("combos[%d] %q groups[%d] %q targets[%d]: unknown account_pool %q", ci, c.Alias, gi, g.Name, ti, t.AccountPool)
					}
				}
			}
		}
	}
	return nil
}
