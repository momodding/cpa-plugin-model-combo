package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRequestCapabilitiesDerivesExplicitRequirements(t *testing.T) {
	required := requestCapabilities(pluginapi.ExecutorRequest{Model: "smart:thinking"}, executionBody{body: []byte(`{
		"tools":[{"type":"function"}],
		"reasoning_effort":"high",
		"response_format":{"json_schema":{"strict":true}},
		"messages":[{"content":[{"type":"image_url"},{"type":"input_audio"},{"type":"input_file"}]}]
	}`)})
	for _, capability := range []modelCapability{
		modelCapabilityReasoning,
		modelCapabilityTools,
		modelCapabilityJSONSchema,
		modelCapabilityVisionInput,
		modelCapabilityAudioInput,
		modelCapabilityDocumentInput,
	} {
		if _, ok := required[capability]; !ok {
			t.Fatalf("missing capability %q in %#v", capability, required)
		}
	}
}

func TestCandidateSupportsCapabilityPolicy(t *testing.T) {
	required := map[modelCapability]struct{}{modelCapabilityTools: {}}
	undeclared := candidatePlan{}
	if !candidateSupports(undeclared, capabilityPolicyCompatibleIfKnown, required) {
		t.Fatal("compatible-if-known rejected undeclared candidate")
	}
	if candidateSupports(undeclared, capabilityPolicyRequireDeclared, required) {
		t.Fatal("require-declared accepted undeclared candidate")
	}
	if candidateSupports(candidatePlan{capabilitiesDeclared: true}, capabilityPolicyCompatibleIfKnown, required) {
		t.Fatal("explicitly empty capability declaration was accepted")
	}
	if candidateSupports(candidatePlan{capabilities: map[modelCapability]struct{}{modelCapabilityReasoning: {}}, capabilitiesDeclared: true}, capabilityPolicyCompatibleIfKnown, required) {
		t.Fatal("candidate missing declared required capability was accepted")
	}
	if !candidateSupports(candidatePlan{capabilities: map[modelCapability]struct{}{modelCapabilityTools: {}}, capabilitiesDeclared: true}, capabilityPolicyRequireDeclared, required) {
		t.Fatal("candidate with declared required capability was rejected")
	}
}

func TestCompileComboRetainsExplicitEmptyCapabilities(t *testing.T) {
	config, err := decodeRouterConfig([]byte(`
combos:
  - alias: smart
    groups:
      - name: primary
        targets:
          - model: provider-a
            capabilities: []
`))
	if err != nil {
		t.Fatalf("decodeRouterConfig() error = %v", err)
	}
	candidate := compileCombos(config.Combos, config.Resilience)[0].groups[0].candidates[0]
	if !candidate.capabilitiesDeclared || candidateSupports(candidate, capabilityPolicyCompatibleIfKnown, map[modelCapability]struct{}{modelCapabilityTools: {}}) {
		t.Fatalf("compiled candidate = %#v", candidate)
	}
}

func TestGroupSignatureTracksCapabilityEligibility(t *testing.T) {
	base := groupPlan{
		name:             "primary",
		strategy:         routeStrategyPriority,
		capabilityPolicy: capabilityPolicyCompatibleIfKnown,
		candidates:       []candidatePlan{{model: "provider-a", weight: 1}},
	}
	explicitEmpty := base
	explicitEmpty.candidates = []candidatePlan{{model: "provider-a", weight: 1, capabilitiesDeclared: true}}
	if groupSignature(base) == groupSignature(explicitEmpty) {
		t.Fatal("omitted and explicit empty capabilities shared runtime signature")
	}

	requireDeclared := base
	requireDeclared.capabilityPolicy = capabilityPolicyRequireDeclared
	if groupSignature(base) == groupSignature(requireDeclared) {
		t.Fatal("capability policy change shared runtime signature")
	}
}
