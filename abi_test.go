package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestABIRegistrationRoutingAndCountTokens(t *testing.T) {
	resetModelComboABIState(t)
	config := []byte(`routes:
  - alias: smart
    models: [provider-a]
`)
	request, err := json.Marshal(lifecycleRequest{ConfigYAML: config})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleModelComboABIMethod(t.Context(), pluginabi.MethodPluginRegister, request)
	if err != nil {
		t.Fatalf("plugin.register error = %v", err)
	}
	var registrationEnvelope abiEnvelope
	if err := json.Unmarshal(raw, &registrationEnvelope); err != nil {
		t.Fatalf("decode registration envelope: %v", err)
	}
	var registration abiRegistration
	if err := json.Unmarshal(registrationEnvelope.Result, &registration); err != nil {
		t.Fatalf("decode registration result: %v", err)
	}
	if !registrationEnvelope.OK || registration.SchemaVersion != registrationSchemaVersion || registration.Metadata.Name != pluginName || registration.Metadata.Version != "0.0.0-dev" || !registration.Capabilities.ModelRegistrar || !registration.Capabilities.ModelRouter || !registration.Capabilities.Executor || !registration.Capabilities.RequestInterceptor || registration.Capabilities.RequestLifecycle || registration.Capabilities.ResponseInterceptor || registration.Capabilities.StreamInterceptor || !registration.Capabilities.UsagePlugin || !registration.Capabilities.ManagementAPI {
		t.Fatalf("registration = %#v", registration)
	}

	routeRequest, err := json.Marshal(modelRouteRPCRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "smart(high)"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = handleModelComboABIMethod(t.Context(), pluginabi.MethodModelRoute, routeRequest)
	if err != nil {
		t.Fatalf("model.route error = %v", err)
	}
	var routeEnvelope abiEnvelope
	if err := json.Unmarshal(raw, &routeEnvelope); err != nil {
		t.Fatalf("decode route envelope: %v", err)
	}
	var route pluginapi.ModelRouteResponse
	if err := json.Unmarshal(routeEnvelope.Result, &route); err != nil {
		t.Fatalf("decode route result: %v", err)
	}
	if !route.Handled || route.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("route = %#v", route)
	}

	usageRequest, err := json.Marshal(pluginapi.UsageRecord{
		Provider: "openai", Model: "provider-a", RequestedAt: time.Now().UTC(), Detail: pluginapi.UsageDetail{InputTokens: 10, OutputTokens: 1, CacheCreationTokens: 2, TotalTokens: 11},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleModelComboABIMethod(t.Context(), pluginabi.MethodUsageHandle, usageRequest); err != nil {
		t.Fatalf("usage.handle error = %v", err)
	}
	modelComboABIState.Lock()
	activePlugin := modelComboABIState.plugin
	modelComboABIState.Unlock()
	page, err := activePlugin.store.Requests(usageFilter{From: time.Now().UTC().Add(-time.Hour), To: time.Now().UTC().Add(time.Hour)}, "time", "desc", 0, 10)
	if err != nil || page.Total != 1 || page.Items[0].TotalTokens != 11 || page.Items[0].CacheCreationTokens != 2 || page.Items[0].EffectiveCacheReadTokens != 0 {
		t.Fatalf("usage records = %#v, %v", page, err)
	}

	raw, err = handleModelComboABIMethod(t.Context(), pluginabi.MethodExecutorCountTokens, nil)
	if err != nil {
		t.Fatalf("executor.count_tokens error = %v", err)
	}
	var countEnvelope abiEnvelope
	if err := json.Unmarshal(raw, &countEnvelope); err != nil {
		t.Fatalf("decode count envelope: %v", err)
	}
	if countEnvelope.OK || countEnvelope.Error == nil || countEnvelope.Error.HTTPStatus != 501 || countEnvelope.Error.Code != "model_route_count_tokens_unsupported" {
		t.Fatalf("count envelope = %#v", countEnvelope)
	}
}

func TestABIRegistrationUsesStreamBodyOmissionWhenHostSupportsIt(t *testing.T) {
	resetModelComboABIState(t)
	request, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("routes: []\n"), SchemaVersion: streamBodyOmissionSchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleModelComboABIMethod(t.Context(), pluginabi.MethodPluginRegister, request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope abiEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var registration abiRegistration
	if err := json.Unmarshal(envelope.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if registration.SchemaVersion != streamBodyOmissionSchemaVersion {
		t.Fatalf("schema version = %d, want %d", registration.SchemaVersion, streamBodyOmissionSchemaVersion)
	}
}

func resetModelComboABIState(t *testing.T) {
	t.Helper()
	previousResolver := defaultDataPathResolver
	testDataPath := filepath.Join(t.TempDir(), defaultDataFileName)
	defaultDataPathResolver = func() string { return testDataPath }
	modelComboABIState.Lock()
	previousPlugin := modelComboABIState.plugin
	modelComboABIState.plugin = nil
	modelComboABIState.metadata = pluginapi.Metadata{}
	modelComboABIState.shuttingDown = false
	modelComboABIState.Unlock()
	if previousPlugin != nil && previousPlugin.store != nil {
		_ = previousPlugin.store.Close()
	}
	t.Cleanup(func() {
		modelComboABIState.Lock()
		plugin := modelComboABIState.plugin
		modelComboABIState.plugin = nil
		modelComboABIState.metadata = pluginapi.Metadata{}
		modelComboABIState.shuttingDown = false
		modelComboABIState.Unlock()
		if plugin != nil && plugin.store != nil {
			_ = plugin.store.Close()
		}
		defaultDataPathResolver = previousResolver
	})
}

func TestABIShutdownPersistsReplacementAndClosesStore(t *testing.T) {
	resetModelComboABIState(t)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	store, err := openUsageStore(dbPath, 30)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.PersistComboHealth(map[string]map[string]map[string]*candidateHealth{
		"obsolete": {"group": {"candidate": {ConsecutiveFailures: 1}}},
	}); err != nil {
		t.Fatalf("seed obsolete health: %v", err)
	}

	// Create a configured plugin with plan/group/candidate for MarkFailure.
	config := []byte(`combos:
  - alias: test-combo
    groups:
      - name: group1
        models: [model-a]
`)
	plugin, _, err := newModelComboPlugin(config, nil)
	if err != nil {
		t.Fatalf("new plugin: %v", err)
	}
	plugin.store = store
	testPlan := plugin.plans[0]
	testGroup := testPlan.groups[0]
	testCandidate := testGroup.candidates[0]

	// Initialize comboRuntime properly with time source and groups.
	plugin.comboRuntime = newComboRuntime(time.Now)
	gkey := runtimeGroupKey(testPlan.aliasKey, testGroup.groupKey)
	sig := groupSignature(testGroup)
	plugin.comboRuntime.groups[gkey] = newGroupRuntime(sig, testGroup)
	plugin.comboRuntime.MarkFailure(testPlan, testGroup, testCandidate, testPlan.resilience)

	// Set ABI state and invoke shutdown.
	modelComboABIState.Lock()
	modelComboABIState.plugin = plugin
	modelComboABIState.metadata = pluginapi.Metadata{Name: "test-model-combo", Version: "1.0.0"}
	modelComboABIState.shuttingDown = false
	modelComboABIState.Unlock()
	ModelComboPluginShutdown()

	// Verify ABI state cleared: plugin detached, metadata reset, shuttingDown set.
	modelComboABIState.Lock()
	abiPlugin := modelComboABIState.plugin
	abiMetadata := modelComboABIState.metadata
	abiShuttingDown := modelComboABIState.shuttingDown
	modelComboABIState.Unlock()

	if abiPlugin != nil {
		t.Fatal("plugin not detached after shutdown")
	}
	if !reflect.DeepEqual(abiMetadata, pluginapi.Metadata{}) {
		t.Fatal("metadata not zeroed after shutdown")
	}
	if !abiShuttingDown {
		t.Fatal("shuttingDown not set after shutdown")
	}

	// Original store must be closed (read fails).
	if _, err := store.LoadComboHealthSnapshot("obsolete", "group", "candidate"); err == nil {
		t.Fatal("closed store accepted health read")
	}

	// Reopen and verify: obsolete row gone (replaced by fresh runtime), new candidate health persists (nonhealthy).
	reopened, err := openUsageStore(dbPath, 30)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	obsoleteSnapshot, err := reopened.LoadComboHealthSnapshot("obsolete", "group", "candidate")
	if err != nil {
		t.Fatalf("load obsolete health: %v", err)
	}
	if obsoleteSnapshot != nil {
		t.Fatal("obsolete health remained after shutdown replacement")
	}
	// New candidate health should persist with failure state.
	newSnapshot, err := reopened.LoadComboHealthSnapshot(testPlan.aliasKey, testGroup.groupKey, testCandidate.candidateKey)
	if err != nil {
		t.Fatalf("load new candidate health: %v", err)
	}
	if newSnapshot == nil || newSnapshot.ConsecutiveFailures == 0 {
		t.Fatalf("new candidate health not persisted with failure state: %#v", newSnapshot)
	}
}
