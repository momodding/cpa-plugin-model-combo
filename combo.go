package main

import (
	"fmt"
	"strings"
)

// comboPlan is the immutable compiled representation of a modelCombo.
// It is built once at config load time and shared across requests without mutation.
type comboPlan struct {
	alias            string
	aliasKey         string // routeKey(alias)
	groups           []groupPlan
	capabilityPolicy capabilityPolicy
	resilience       resilienceConfig
	maxAttempts      int
}

// groupPlan is the immutable compiled representation of one ordered group.
type groupPlan struct {
	name             string
	groupKey         string // routeKey(name) within combo
	index            int
	strategy         string
	cooldownSec      int
	capabilityPolicy capabilityPolicy
	candidates       []candidatePlan
	weightTotal      int64 // sum of weights; 0 for priority/round-robin
}

// candidatePlan is an immutable target within a group.
type candidatePlan struct {
	model                string
	weight               int
	capabilities         map[modelCapability]struct{}
	capabilitiesDeclared bool
	accountPool          *accountPlan
	// candidateKey is the health-tracking key: comboAliasKey+"\x00"+groupKey+"\x00"+normalizedModel
	candidateKey string
}

// comboSignature returns a stable string that changes when plan structure changes.
func comboSignature(p comboPlan) string {
	var sb strings.Builder
	sb.WriteString(p.alias)
	sb.WriteString("\x00")
	sb.WriteString(string(p.capabilityPolicy))
	for _, g := range p.groups {
		sb.WriteString("\x00")
		sb.WriteString(groupSignature(g))
	}
	return sb.String()
}

// groupSignature returns a stable string for one group (used for state preservation on reload).
func groupSignature(g groupPlan) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\x00%s\x00%d\x00%d\x00%s", g.name, g.strategy, g.cooldownSec, g.weightTotal, g.capabilityPolicy)
	for _, c := range g.candidates {
		fmt.Fprintf(&sb, "\x01%s\x01%d\x01%t", c.model, c.weight, c.capabilitiesDeclared)
		if c.accountPool != nil {
			fmt.Fprintf(&sb, "\x01%s", c.accountPool.signature)
		}
		for _, capability := range []modelCapability{modelCapabilityReasoning, modelCapabilityTools, modelCapabilityVisionInput, modelCapabilityAudioInput, modelCapabilityDocumentInput, modelCapabilityJSONSchema} {
			if _, supported := c.capabilities[capability]; supported {
				fmt.Fprintf(&sb, "\x01%s", capability)
			}
		}
	}
	return sb.String()
}

// compileCombos converts decoded []modelCombo into []comboPlan.
func compileCombos(combos []modelCombo, resilience resilienceConfig) []comboPlan {
	plans := make([]comboPlan, 0, len(combos))
	for _, combo := range combos {
		plans = append(plans, compileCombo(combo, resilience))
	}
	return plans
}

// compileCombosWithPools resolves each optional target pool once at reload.
func compileCombosWithPools(combos []modelCombo, resilience resilienceConfig, pools map[string]accountPool) []comboPlan {
	plans := compileCombos(combos, resilience)
	for comboIndex := range combos {
		for groupIndex := range combos[comboIndex].Groups {
			for targetIndex, target := range combos[comboIndex].Groups[groupIndex].Targets {
				if target.AccountPool == "" {
					continue
				}
				pool, ok := pools[routeKey(target.AccountPool)]
				if !ok {
					continue
				}
				plans[comboIndex].groups[groupIndex].candidates[targetIndex].accountPool = newAccountPlan(pool)
			}
		}
	}
	return plans
}

func compileCombo(combo modelCombo, resilience resilienceConfig) comboPlan {
	ak := routeKey(combo.Alias)
	groups := make([]groupPlan, 0, len(combo.Groups))
	for idx, g := range combo.Groups {
		gk := routeKey(g.Name)
		candidates := make([]candidatePlan, 0, len(g.Targets))
		var weightTotal int64
		for _, t := range g.Targets {
			base, _, _ := splitThinkingSuffix(t.Model)
			ck := ak + "\x00" + gk + "\x00" + routeKey(base)
			caps := make(map[modelCapability]struct{}, len(t.Capabilities))
			for _, c := range t.Capabilities {
				caps[c] = struct{}{}
			}
			candidates = append(candidates, candidatePlan{
				model:                t.Model,
				weight:               t.Weight,
				capabilities:         caps,
				capabilitiesDeclared: t.CapabilitiesDeclared,
				candidateKey:         ck,
			})
			if t.Weight > 0 {
				weightTotal += int64(t.Weight)
			}
		}
		gp := groupPlan{
			name:             g.Name,
			groupKey:         gk,
			index:            idx,
			strategy:         g.Strategy,
			cooldownSec:      g.CooldownSeconds,
			capabilityPolicy: combo.CapabilityPolicy,
			candidates:       candidates,
			weightTotal:      weightTotal,
		}
		groups = append(groups, gp)
	}
	maxAttempts := resilience.MaxAttempts
	if combo.legacy && len(groups) == 1 && len(groups[0].candidates) < maxAttempts {
		maxAttempts = len(groups[0].candidates)
	}
	return comboPlan{
		alias:            combo.Alias,
		aliasKey:         ak,
		groups:           groups,
		capabilityPolicy: combo.CapabilityPolicy,
		resilience:       resilience,
		maxAttempts:      maxAttempts,
	}
}

// lookupCombo finds a compiled plan by requested model alias (thinking-suffix aware).
func lookupCombo(plans []comboPlan, requestedModel string) (comboPlan, bool) {
	base, _, _ := splitThinkingSuffix(requestedModel)
	key := routeKey(base)
	for _, p := range plans {
		if p.aliasKey == key {
			return p, true
		}
	}
	return comboPlan{}, false
}
