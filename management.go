package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	modelComboDashboardPath   = "/v0/resource/plugins/" + pluginID + "/config"
	modelComboValidationPath  = "/v0/management/plugins/" + pluginID + "/validate"
	modelComboUsageBasePath   = "/v0/management/plugins/" + pluginID + "/usage"
	maxManagementRequestBytes = 1 << 20
)

type managementRPCRequest struct {
	pluginapi.ManagementRequest
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type modelComboValidationRequest struct {
	Enabled      *bool                      `json:"enabled"`
	Routes       []modelRouteYAML           `json:"routes"`
	Combos       []modelComboYAML           `json:"combos"`
	AccountPools map[string]accountPoolYAML `json:"account_pools"`
}

func modelComboManagementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodPost, Path: "/plugins/" + pluginID + "/validate", Description: "Validate Model Combo configuration before saving it."},
			{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/usage/dashboard", Description: "Read usage summaries and both tables from one snapshot."},
			{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/usage/overview", Description: "Read usage summaries, trends, costs, and model breakdowns."},
			{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/usage/groups", Description: "Read paginated usage dimension groups."},
			{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/usage/requests", Description: "Read paginated request-level usage."},
			{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/usage/prices", Description: "Read the persisted model price book."},
			{Method: http.MethodPut, Path: "/plugins/" + pluginID + "/usage/prices", Description: "Persist a revision-protected model price book."},
			{Method: http.MethodPost, Path: "/plugins/" + pluginID + "/usage/prices/sync", Description: "Synchronize CPA model prices from models.dev."},
			{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/usage/preferences", Description: "Read usage dashboard preferences."},
			{Method: http.MethodPut, Path: "/plugins/" + pluginID + "/usage/preferences", Description: "Persist usage dashboard preferences."},
			{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/health/list", Description: "List all persisted combo health snapshots."},
			{Method: http.MethodPost, Path: "/plugins/" + pluginID + "/health/reset", Description: "Clear health state for a combo group by alias and group key."},
		},
		Resources: []resourceRoute{{
			Path:        "/config",
			Menu:        "Model Combo",
			Description: "Configure logical aliases and ordered model target pools.",
		}},
	}
}

func handleModelComboManagement(plugin *modelComboPlugin, request pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	path := strings.TrimRight(strings.TrimSpace(request.Path), "/")
	if strings.HasPrefix(path, modelComboUsageBasePath+"/") {
		return handleUsageManagement(plugin, path, request)
	}
	if strings.HasPrefix(path, "/v0/management/plugins/"+pluginID+"/health/") {
		return handleHealthManagement(plugin, path, request)
	}
	switch path {
	case modelComboDashboardPath:
		if !strings.EqualFold(request.Method, http.MethodGet) {
			return modelComboJSONResponse(http.StatusMethodNotAllowed, map[string]any{
				"error":   "method_not_allowed",
				"message": "dashboard only supports GET",
			})
		}
		if configDashboardError != nil {
			return modelComboJSONResponse(http.StatusInternalServerError, map[string]any{
				"error":   "dashboard_render_failed",
				"message": configDashboardError.Error(),
			})
		}
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":            {"text/html; charset=utf-8"},
				"Cache-Control":           {"no-store"},
				"Content-Security-Policy": {"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'"},
				"Referrer-Policy":         {"no-referrer"},
				"X-Content-Type-Options":  {"nosniff"},
			},
			Body: append([]byte(nil), configDashboardHTML...),
		}
	case modelComboValidationPath:
		if !strings.EqualFold(request.Method, http.MethodPost) {
			return modelComboJSONResponse(http.StatusMethodNotAllowed, map[string]any{
				"error":   "method_not_allowed",
				"message": "validation only supports POST",
			})
		}
		return validateModelComboManagementConfig(request.Body)
	default:
		return modelComboJSONResponse(http.StatusNotFound, map[string]any{
			"error":   "not_found",
			"message": "management resource not found",
		})
	}
}

func validateModelComboManagementConfig(body []byte) pluginapi.ManagementResponse {
	if len(body) > maxManagementRequestBytes {
		return modelComboJSONResponse(http.StatusRequestEntityTooLarge, map[string]any{
			"valid":   false,
			"error":   "request_too_large",
			"message": "configuration exceeds the 1 MiB validation limit",
		})
	}
	var request modelComboValidationRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return modelComboJSONResponse(http.StatusBadRequest, map[string]any{
			"valid":   false,
			"error":   "invalid_request",
			"message": err.Error(),
		})
	}
	if err := ensureModelComboJSONEOF(decoder); err != nil {
		return modelComboJSONResponse(http.StatusBadRequest, map[string]any{
			"valid":   false,
			"error":   "invalid_request",
			"message": err.Error(),
		})
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	wire := struct {
		Enabled      bool                       `yaml:"enabled"`
		Routes       []modelRouteYAML           `yaml:"routes"`
		Combos       []modelComboYAML           `yaml:"combos"`
		AccountPools map[string]accountPoolYAML `yaml:"account_pools"`
	}{
		Enabled:      enabled,
		Routes:       request.Routes,
		Combos:       request.Combos,
		AccountPools: request.AccountPools,
	}
	raw, err := yaml.Marshal(wire)
	if err != nil {
		return modelComboJSONResponse(http.StatusInternalServerError, map[string]any{
			"valid":   false,
			"error":   "validation_failed",
			"message": err.Error(),
		})
	}
	if _, err := decodeRouterConfig(raw); err != nil {
		return modelComboJSONResponse(http.StatusBadRequest, map[string]any{
			"valid":   false,
			"error":   "invalid_config",
			"message": err.Error(),
		})
	}
	return modelComboJSONResponse(http.StatusOK, map[string]any{
		"valid":       true,
		"route_count": len(request.Routes),
	})
}

func ensureModelComboJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not supported")
		}
		return err
	}
	return nil
}

func modelComboJSONResponse(status int, value any) pluginapi.ManagementResponse {
	body, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"response_encode_failed"}`)
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":           {"application/json; charset=utf-8"},
			"Cache-Control":          {"no-store"},
			"X-Content-Type-Options": {"nosniff"},
		},
		Body: body,
	}
}

// handleHealthManagement routes health management endpoints.
func handleHealthManagement(plugin *modelComboPlugin, path string, request pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	if plugin == nil || plugin.store == nil {
		return modelComboJSONResponse(http.StatusServiceUnavailable, map[string]any{
			"error":   "plugin_unavailable",
			"message": "usage store not initialized",
		})
	}

	// Exact path matching for health endpoints.
	healthListPath := "/v0/management/plugins/" + pluginID + "/health/list"
	healthResetPath := "/v0/management/plugins/" + pluginID + "/health/reset"

	switch path {
	case healthListPath:
		if !strings.EqualFold(request.Method, http.MethodGet) {
			return modelComboJSONResponse(http.StatusMethodNotAllowed, map[string]any{
				"error":   "method_not_allowed",
				"message": "health list only supports GET",
			})
		}
		return handleHealthList(plugin)
	case healthResetPath:
		if !strings.EqualFold(request.Method, http.MethodPost) {
			return modelComboJSONResponse(http.StatusMethodNotAllowed, map[string]any{
				"error":   "method_not_allowed",
				"message": "health reset only supports POST",
			})
		}
		return handleHealthReset(plugin, request.Body)
	default:
		return modelComboJSONResponse(http.StatusNotFound, map[string]any{
			"error":   "not_found",
			"message": "health endpoint not found",
		})
	}
}

type healthListResponse struct {
	ComboHealth map[string]map[string]map[string]map[string]any `json:"comboHealth"`
	Count       int                                             `json:"count"`
}

// handleHealthList returns all persisted combo health snapshots.
func handleHealthList(plugin *modelComboPlugin) pluginapi.ManagementResponse {
	snapshot, err := plugin.store.HydrateComboHealth()
	if err != nil {
		return modelComboJSONResponse(http.StatusInternalServerError, map[string]any{
			"error":   "load_failed",
			"message": fmt.Sprintf("failed to load health: %v", err),
		})
	}

	// Convert to displayable format without raw secrets/keys.
	displaySnapshot := make(map[string]map[string]map[string]map[string]any)
	count := 0
	for comboKey, groupMap := range snapshot {
		displaySnapshot[comboKey] = make(map[string]map[string]map[string]any)
		for groupKey, candMap := range groupMap {
			displaySnapshot[comboKey][groupKey] = make(map[string]map[string]any)
			for candidateKey, health := range candMap {
				if health != nil {
					displaySnapshot[comboKey][groupKey][candidateKey] = map[string]any{
						"coolUntil":           health.CoolUntil.Format(time.RFC3339Nano),
						"consecutiveFailures": health.ConsecutiveFailures,
						"circuitOpen":         health.CircuitOpen,
						"circuitOpenUntil":    health.CircuitOpenUntil.Format(time.RFC3339Nano),
						"circuitReopenCount":  health.CircuitReopenCount,
						"halfOpenOccupied":    health.HalfOpenOccupied,
					}
					count++
				}
			}
		}
	}

	return modelComboJSONResponse(http.StatusOK, healthListResponse{
		ComboHealth: displaySnapshot,
		Count:       count,
	})
}

type healthResetRequest struct {
	ComboAliasKey string `json:"comboAliasKey"`
	GroupKey      string `json:"groupKey"`
}

// handleHealthReset clears health state for a scoped combo group.
func handleHealthReset(plugin *modelComboPlugin, body []byte) pluginapi.ManagementResponse {
	var req healthResetRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return modelComboJSONResponse(http.StatusBadRequest, map[string]any{
			"error":   "decode_failed",
			"message": fmt.Sprintf("invalid request: %v", err),
		})
	}
	if err := ensureModelComboJSONEOF(decoder); err != nil {
		return modelComboJSONResponse(http.StatusBadRequest, map[string]any{
			"error":   "extra_data",
			"message": fmt.Sprintf("trailing data: %v", err),
		})
	}

	if req.ComboAliasKey == "" || req.GroupKey == "" {
		return modelComboJSONResponse(http.StatusBadRequest, map[string]any{
			"error":   "missing_keys",
			"message": "comboAliasKey and groupKey are required",
		})
	}

	// Drain and cancel queued writes before deleting persisted state.
	if err := plugin.comboRuntime.ResetComboGroupHealthAndPersist(req.ComboAliasKey, req.GroupKey, func() error {
		if plugin.healthPersistence != nil {
			if err := plugin.healthPersistence.Flush(); err != nil {
				return err
			}
			plugin.healthPersistence.Cancel(req.ComboAliasKey, req.GroupKey)
		}
		return plugin.store.ResetComboHealth(req.ComboAliasKey, req.GroupKey)
	}); err != nil {
		return modelComboJSONResponse(http.StatusInternalServerError, map[string]any{
			"error":   "reset_failed",
			"message": fmt.Sprintf("failed to reset health: %v", err),
		})
	}
	return modelComboJSONResponse(http.StatusOK, map[string]any{
		"status":        "cleared",
		"comboAliasKey": req.ComboAliasKey,
		"groupKey":      req.GroupKey,
	})
}
