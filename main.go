package main

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginID   = "model-combo"
	pluginName = "Model Combo"
)

var pluginVersion = "0.0.0-dev"

type modelComboPlugin struct {
	config           routerConfig
	plans            []comboPlan
	runtime          *routeRuntime // legacy flat-route execution path
	comboRuntime     *comboRuntime // combo-group execution path
	store            *usageStore
	attribution      *attributionTracker
	modelsDevFetcher modelsDevCatalogFetcher
	priceSync        *priceSyncState
}

var (
	_ pluginapi.ModelRouter        = (*modelComboPlugin)(nil)
	_ pluginapi.ModelRegistrar     = (*modelComboPlugin)(nil)
	_ pluginapi.ProviderExecutor   = (*modelComboPlugin)(nil)
	_ pluginapi.RequestInterceptor = (*modelComboPlugin)(nil)
	_ pluginapi.UsagePlugin        = (*modelComboPlugin)(nil)
)

func newModelComboPlugin(configYAML []byte, previous *modelComboPlugin) (*modelComboPlugin, pluginapi.Metadata, error) {
	cfg, err := decodeRouterConfig(configYAML)
	if err != nil {
		return nil, pluginapi.Metadata{}, err
	}
	plans := compileCombosWithPools(cfg.Combos, cfg.Resilience, cfg.AccountPools)
	legacyRuntime := newRouteRuntime(nil)
	cr := newComboRuntime(nil)
	var store *usageStore
	attribution := newAttributionTracker(nil)
	var fetcher modelsDevCatalogFetcher
	priceSync := &priceSyncState{}
	if previous != nil {
		legacyRuntime = previous.runtime.Clone(cfg.Routes)
		cr = previous.comboRuntime.Clone(plans, nil)
		store = previous.store
		attribution = previous.attribution
		fetcher = previous.modelsDevFetcher
		priceSync = previous.priceSync
		if priceSync == nil {
			priceSync = &priceSyncState{}
		}
		priceSync.mu.Lock()
		if err := store.Reconfigure(cfg.DataPath, cfg.RetentionDays); err != nil {
			priceSync.mu.Unlock()
			return nil, pluginapi.Metadata{}, err
		}
		priceSync.mu.Unlock()
	} else {
		legacyRuntime.Sync(cfg.Routes)
		cr.Sync(plans)
		store, err = openUsageStore(cfg.DataPath, cfg.RetentionDays)
		if err != nil {
			return nil, pluginapi.Metadata{}, err
		}
	}
	plugin := &modelComboPlugin{
		config:           cfg,
		plans:            plans,
		runtime:          legacyRuntime,
		comboRuntime:     cr,
		store:            store,
		attribution:      attribution,
		modelsDevFetcher: fetcher,
		priceSync:        priceSync,
	}
	metadata := pluginapi.Metadata{
		Name:             pluginName,
		Version:          pluginVersion,
		Author:           "momodding",
		GitHubRepository: "https://github.com/momodding/cpa-model-combo",
		ConfigFields: []pluginapi.ConfigField{
			{Name: "combos", Type: pluginapi.ConfigFieldTypeArray, Description: "Ordered combo groups with independent strategy, cooldown, and candidate targets."},
			{Name: "routes", Type: pluginapi.ConfigFieldTypeArray, Description: "Legacy flat routes; each normalizes to one combo with one group named default."},
			{Name: "data_path", Type: pluginapi.ConfigFieldTypeString, Description: "SQLite usage database path; defaults to model-combo-usage.db in the discovered CPA plugins directory."},
			{Name: "retention_days", Type: pluginapi.ConfigFieldTypeInteger, Description: "Number of UTC days of usage aggregates and request details to retain (1-3650)."},
			{Name: "resilience", Type: pluginapi.ConfigFieldTypeObject, Description: "Attempt budget, deadline, backoff, and circuit breaker controls."},
			{Name: "fallback", Type: pluginapi.ConfigFieldTypeObject, Description: "Status codes that move a routed request to the next target."},
		},
	}
	return plugin, metadata, nil
}

func (p *modelComboPlugin) Identifier() string { return pluginID }

func (p *modelComboPlugin) Execute(_ context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{}, fmt.Errorf("%s execution requires the native host callback context", pluginID)
}

func (p *modelComboPlugin) ExecuteStream(_ context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	return pluginapi.ExecutorStreamResponse{}, fmt.Errorf("%s streaming requires the native stream bridge", pluginID)
}

func (p *modelComboPlugin) CountTokens(_ context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{}, newRouteError(501, "model_route_count_tokens_unsupported", "", "token counting for routed aliases is not supported by the current CLIProxyAPI plugin ABI", "")
}

func (p *modelComboPlugin) HttpRequest(_ context.Context, _ pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	return pluginapi.ExecutorHTTPResponse{}, fmt.Errorf("%s does not implement executor.http_request", pluginID)
}

func (p *modelComboPlugin) HandleUsage(_ context.Context, record pluginapi.UsageRecord) {
	if p == nil || !p.config.Enabled || p.store == nil {
		return
	}
	attribution := attributionResult{Kind: attributionUnresolved}
	if p.attribution != nil {
		attribution = p.attribution.Match(record)
	}
	_ = p.store.Record(storedRecordFromUsage(record, attribution))
}

func (p *modelComboPlugin) InterceptRequestBeforeAuth(_ context.Context, request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if p == nil || !p.config.Enabled || p.attribution == nil {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	if _, routed := p.matchingCombo(request.RequestedModel); routed {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	p.attribution.MarkDirectRequest(request)
	return pluginapi.RequestInterceptResponse{}, nil
}

func (p *modelComboPlugin) InterceptRequestAfterAuth(_ context.Context, request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if p != nil && p.config.Enabled && p.attribution != nil {
		p.attribution.updateDirectRequest(request)
	}
	return pluginapi.RequestInterceptResponse{}, nil
}

func main() {}
