package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	modelRouterDashboardPath        = "/v0/resource/plugins/" + pluginID + "/config"
	modelRouterValidationPath       = "/v0/management/plugins/" + pluginID + "/validate"
	modelRouterAccountsPath         = "/v0/management/plugins/" + pluginID + "/accounts"
	modelRouterQuotaDiagnosticsPath = "/v0/management/plugins/" + pluginID + "/quota-status"
	modelRouterUsageBasePath        = "/v0/management/plugins/" + pluginID + "/usage"
	maxManagementRequestBytes       = 1 << 20
)

func isModelRouterDashboardPath(path string) bool {
	return path == modelRouterDashboardPath
}

type managementRPCRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
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

type modelRouterValidationRequest struct {
	Enabled  *bool               `json:"enabled"`
	Routes   []modelRouteYAML    `json:"routes"`
	Fallback *fallbackConfigYAML `json:"fallback"`
}

func modelRouterManagementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/accounts", Description: "Read available account models."},
			{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/quota-status", Description: "Read quota polling diagnostics."},
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
			{Method: http.MethodPost, Path: "/plugins/" + pluginID + "/usage/reset", Description: "Clear usage aggregates and request history while preserving prices and preferences."},
		},
		Resources: []resourceRoute{{
			Path:        "/config",
			Menu:        pluginName,
			Description: "Configure Model Combo logical aliases and ordered model target pools.",
		}},
	}
}

func handleModelRouterManagement(plugin *modelRouterPlugin, request pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	return handleModelRouterManagementWithHost(plugin, request, nil)
}

func handleModelRouterManagementWithHost(plugin *modelRouterPlugin, request pluginapi.ManagementRequest, host modelHost) pluginapi.ManagementResponse {
	path := strings.TrimRight(strings.TrimSpace(request.Path), "/")
	if strings.HasPrefix(path, modelRouterUsageBasePath+"/") {
		return handleUsageManagement(plugin, path, request)
	}
	if isModelRouterDashboardPath(request.Path) {
		if !strings.EqualFold(request.Method, http.MethodGet) {
			return modelRouterJSONResponse(http.StatusMethodNotAllowed, map[string]any{
				"error":   "method_not_allowed",
				"message": "dashboard only supports GET",
			})
		}
		if configDashboardError != nil {
			return modelRouterJSONResponse(http.StatusInternalServerError, map[string]any{
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
	}
	switch path {
	case modelRouterQuotaDiagnosticsPath:
		if !strings.EqualFold(request.Method, http.MethodGet) {
			return modelRouterJSONResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed", "message": "quota diagnostics only supports GET"})
		}
		return modelRouterJSONResponse(http.StatusOK, quotaDiagnostics(plugin))
	case modelRouterAccountsPath:
		if !strings.EqualFold(request.Method, http.MethodGet) {
			return modelRouterJSONResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed", "message": "accounts only supports GET"})
		}
		credentials, err := plugin.accountCatalog(host)
		if err != nil {
			return modelRouterCatalogUnavailable()
		}
		return modelRouterJSONResponse(http.StatusOK, map[string]any{"credentials": credentials})
	case modelRouterValidationPath:
		if !strings.EqualFold(request.Method, http.MethodPost) {
			return modelRouterJSONResponse(http.StatusMethodNotAllowed, map[string]any{
				"error":   "method_not_allowed",
				"message": "validation only supports POST",
			})
		}
		return validateModelRouterManagementConfig(plugin, host, request.Body)
	default:
		return modelRouterJSONResponse(http.StatusNotFound, map[string]any{
			"error":   "not_found",
			"message": "management resource not found",
		})
	}
}

func validateModelRouterManagementConfig(plugin *modelRouterPlugin, host modelHost, body []byte) pluginapi.ManagementResponse {
	if len(body) > maxManagementRequestBytes {
		return modelRouterJSONResponse(http.StatusRequestEntityTooLarge, map[string]any{
			"valid":   false,
			"error":   "request_too_large",
			"message": "configuration exceeds the 1 MiB validation limit",
		})
	}
	var request modelRouterValidationRequest
	if err := decodeManagementJSON(body, maxManagementRequestBytes, &request); err != nil {
		if field, message := allowedAccountsTypeError(body); field != "" {
			return modelRouterFieldError(field, message)
		}
		return modelRouterJSONResponse(http.StatusBadRequest, map[string]any{
			"valid":   false,
			"error":   "invalid_request",
			"message": err.Error(),
		})
	}
	// Validate saved routes even when routing is disabled.
	wire := struct {
		Enabled  bool                `yaml:"enabled"`
		Routes   []modelRouteYAML    `yaml:"routes"`
		Fallback *fallbackConfigYAML `yaml:"fallback,omitempty"`
	}{Enabled: true, Routes: request.Routes, Fallback: request.Fallback}
	raw, err := yaml.Marshal(wire)
	if err != nil {
		return modelRouterJSONResponse(http.StatusInternalServerError, map[string]any{
			"valid":   false,
			"error":   "validation_failed",
			"message": err.Error(),
		})
	}
	config, err := decodeRouterConfig(raw)
	if err != nil {
		message := err.Error()
		if strings.HasPrefix(message, "routes[") {
			if end := strings.Index(message, ":"); end > 0 && strings.Contains(message[:end], ".allowed_accounts[") {
				return modelRouterFieldError(message[:end], strings.TrimSpace(message[end+1:]))
			}
		}
		return modelRouterJSONResponse(http.StatusBadRequest, map[string]any{
			"valid":   false,
			"error":   "invalid_config",
			"message": err.Error(),
		})
	}
	var credentials []accountCredential
	for routeIndex, route := range config.Routes {
		if len(route.AllowedAccounts) == 0 {
			continue
		}
		if credentials == nil {
			credentials, err = plugin.accountCatalog(host)
			if err != nil {
				return modelRouterCatalogUnavailable()
			}
			if credentials == nil {
				credentials = []accountCredential{}
			}
		}
		selected := make([]accountCredential, 0, len(route.AllowedAccounts))
		for accountIndex, id := range route.AllowedAccounts {
			field := fmt.Sprintf("routes[%d].allowed_accounts[%d]", routeIndex, accountIndex)
			var found *accountCredential
			for i := range credentials {
				if credentials[i].ID == id {
					found = &credentials[i]
					break
				}
			}
			if found == nil || found.Disabled {
				return modelRouterFieldError(field, "account does not exist or is disabled")
			}
			supported := false
			for _, target := range route.Targets {
				if found.supportsModel(target.Model) {
					supported = true
					break
				}
			}
			if !supported {
				return modelRouterFieldError(field, "account does not support any route target")
			}
			selected = append(selected, *found)
		}
		for targetIndex, target := range route.Targets {
			supported := false
			for _, credential := range selected {
				if credential.supportsModel(target.Model) {
					supported = true
					break
				}
			}
			if !supported {
				field := "targets"
				if request.Routes[routeIndex].Models != nil {
					field = "models"
				}
				return modelRouterFieldError(fmt.Sprintf("routes[%d].%s[%d]", routeIndex, field, targetIndex), "target is not offered by any selected account")
			}
		}
	}
	return modelRouterJSONResponse(http.StatusOK, map[string]any{
		"valid":       true,
		"route_count": len(request.Routes),
	})
}

func modelRouterCatalogUnavailable() pluginapi.ManagementResponse {
	return modelRouterJSONResponse(http.StatusServiceUnavailable, map[string]any{"valid": false, "error": "catalog_unavailable", "message": "account catalog unavailable"})
}

func modelRouterFieldError(field, message string) pluginapi.ManagementResponse {
	return modelRouterJSONResponse(http.StatusBadRequest, map[string]any{"valid": false, "error": "invalid_config", "field": field, "message": field + ": " + message})
}

func modelRouterJSONResponse(status int, value any) pluginapi.ManagementResponse {
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

// allowedAccountsTypeError locates the first allowed_accounts value that is not an array of strings.
func allowedAccountsTypeError(body []byte) (string, string) {
	var wire struct {
		Routes []struct {
			AllowedAccounts json.RawMessage `json:"allowed_accounts"`
		} `json:"routes"`
	}
	if json.Unmarshal(body, &wire) != nil {
		return "", ""
	}
	for routeIndex, route := range wire.Routes {
		field := fmt.Sprintf("routes[%d].allowed_accounts", routeIndex)
		var items []json.RawMessage
		if len(route.AllowedAccounts) == 0 || string(route.AllowedAccounts) == "null" {
			continue
		}
		if json.Unmarshal(route.AllowedAccounts, &items) != nil {
			return field, "allowed_accounts must be an array of strings"
		}
		for itemIndex, item := range items {
			if len(item) > 0 && item[0] != '"' && string(item) != "null" {
				return fmt.Sprintf("%s[%d]", field, itemIndex), "account ID must be a string"
			}
		}
	}
	return "", ""
}
