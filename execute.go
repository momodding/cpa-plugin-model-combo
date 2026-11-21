package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type modelHost interface {
	Execute(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error)
	StartStream(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error)
	ReadStream(string) (pluginapi.HostModelStreamReadResponse, error)
	CloseStream(string) error
	Emit(string, []byte) error
	ClosePluginStream(string, string)
}

func (p *modelRouterPlugin) executeWithHost(request pluginapi.ExecutorRequest, host modelHost) (pluginapi.ExecutorResponse, error) {
	if p == nil || host == nil {
		return pluginapi.ExecutorResponse{}, statusError{status: http.StatusBadGateway, message: "model router host callback is unavailable"}
	}
	route, ok := p.matchingRoute(request.Model)
	if !ok {
		return pluginapi.ExecutorResponse{}, statusError{status: http.StatusBadGateway, message: "no model route matched executor request"}
	}
	requestedModel := strings.TrimSpace(request.Model)
	bodyInfo := bodyForExecution(request)
	routerRequestID := rand.Text()
	policy := p.config.fallbackPolicy()
	accounts, unavailable, err := p.routeAccounts(route, requestedModel, host)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	if route.Strategy == routeStrategyHeadroom {
		return p.executeHeadroom(request, host, route, bodyInfo, routerRequestID, accounts, unavailable)
	}
	var lastErr error
	var lastTarget string
	lastStatus := 0
	attempted := make(map[string]struct{}, len(route.Targets))
	for key := range unavailable {
		attempted[key] = struct{}{}
	}
	limit := p.config.attemptLimit(len(route.Targets))
	for attempt := 0; attempt < limit; attempt++ {
		selection := p.runtime.SelectExcluding(route, attempted)
		if selection.allCooling {
			return pluginapi.ExecutorResponse{}, cooldownRouteError(route, selection.retryAfter)
		}
		if selection.model == "" {
			break
		}
		attempted[routeKey(selection.model)] = struct{}{}
		target := targetModel(requestedModel, selection.model)
		candidates := accounts[routeKey(selection.model)]
		if len(route.AllowedAccounts) == 0 {
			candidates = []accountCredential{{}}
		}
		for _, account := range candidates {
			p.attribution.MarkRouted(route.Alias, target, request.Headers, routerRequestID, attempt)
			response, err := host.Execute(hostRequest(request, bodyInfo, target, false, account))
			status := response.StatusCode
			if status == 0 && err == nil {
				status = http.StatusOK
			}
			if status == 0 && err != nil {
				status = statusFromError(err)
			}
			if err == nil && status >= 200 && status < 300 {
				p.runtime.MarkSuccess(route, selection.model)
				metadata := map[string]any{
					"route_alias":    route.Alias,
					"selected_model": target,
					"selected_index": attempt,
				}
				if lastTarget != "" {
					metadata["fallback_attempt"] = attempt
					metadata["fallback_from"] = lastTarget
					metadata["fallback_reason"] = fallbackReason(lastStatus, lastErr)
				}
				return pluginapi.ExecutorResponse{
					Payload:  rewriteResponseModel(response.Body, requestedModel),
					Headers:  cloneHeader(response.Headers),
					Metadata: metadata,
				}, nil
			}
			if err == nil {
				err = statusError{status: status, message: hostStatusMessage(target, status, response.Body)}
			}
			if err != nil && terminalRequestError(err) {
				// Cancellation and deadline stop immediately, whatever response status or fallback policy accompanies them.
				if account.ID != "" {
					err = safeRestrictedFailure(err)
				}
				return pluginapi.ExecutorResponse{}, err
			}
			retry := eligibleRouteFailure(err, policy)
			if account.ID != "" {
				err = safeRestrictedFailure(err)
			}
			if !retry || p.config.FallbackDisabled {
				return pluginapi.ExecutorResponse{}, err
			}
			lastErr = err
			lastTarget = target
			lastStatus = status
		}
		p.runtime.MarkFailure(route, selection.model)
	}
	detail := ""
	if lastErr != nil {
		detail = lastErr.Error()
	}
	return pluginapi.ExecutorResponse{}, newRouteError(http.StatusServiceUnavailable, "model_route_unavailable", route.Alias, fmt.Sprintf("no available candidate for model route %q", route.Alias), detail)
}

// routeAccounts snapshots the eligible accounts once and excludes targets with no compatible account.
func (p *modelRouterPlugin) routeAccounts(route modelRoute, requestedModel string, host modelHost) (map[string][]accountCredential, map[string]struct{}, error) {
	if len(route.AllowedAccounts) == 0 && route.Strategy != routeStrategyHeadroom {
		return nil, nil, nil
	}
	catalog, err := p.accountCatalog(host)
	if err != nil {
		return nil, nil, newRouteError(http.StatusServiceUnavailable, "model_route_unavailable", route.Alias, fmt.Sprintf("no available candidate for model route %q", route.Alias), "credential catalog unavailable")
	}
	byID := make(map[string]accountCredential, len(catalog))
	for _, account := range catalog {
		if !account.Disabled && account.ID != "" && account.Provider != "" {
			byID[account.ID] = account
		}
	}
	order := route.AllowedAccounts
	if len(order) == 0 {
		for _, account := range catalog {
			order = append(order, account.ID)
		}
	}
	eligible := make(map[string][]accountCredential, len(route.Targets))
	unavailable := make(map[string]struct{})
	for _, target := range route.Targets {
		key := routeKey(target.Model)
		for _, id := range order {
			if account, ok := byID[id]; ok && account.supportsModel(targetModel(requestedModel, target.Model)) {
				eligible[key] = append(eligible[key], account)
			}
		}
		if len(eligible[key]) == 0 {
			unavailable[key] = struct{}{}
		}
	}
	return eligible, unavailable, nil
}

func safeAccountFailure(status int) error {
	if status == 0 {
		return statusError{message: "host account execution failed"}
	}
	return statusError{status: status, message: fmt.Sprintf("host account execution failed (status %d)", status)}
}

func safeRestrictedFailure(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if terminalRequestError(err) {
		return context.Canceled
	}
	return safeAccountFailure(statusFromError(err))
}

func hostRequest(request pluginapi.ExecutorRequest, bodyInfo executionBody, model string, stream bool, account accountCredential) pluginapi.HostModelExecutionRequest {
	return pluginapi.HostModelExecutionRequest{
		EntryProtocol:  bodyInfo.entryProtocol,
		ExitProtocol:   bodyInfo.responseProtocol,
		Model:          model,
		Stream:         stream,
		Body:           requestBodyForTarget(bodyInfo.body, model),
		Headers:        sanitizeNestedHeaders(request.Headers),
		Query:          cloneValues(request.Query),
		Alt:            request.Alt,
		AuthID:         account.ID,
		ForcedProvider: account.Provider,
	}
}

func hostStatusMessage(model string, status int, body []byte) string {
	message := fmt.Sprintf("host model %s returned status %d", model, status)
	detail := strings.TrimSpace(string(body))
	if len(detail) > 512 {
		detail = detail[:512]
	}
	if detail != "" {
		message += ": " + detail
	}
	return message
}

func cooldownRouteError(route modelRoute, retryAfter time.Duration) error {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return newRouteError(http.StatusTooManyRequests, "model_route_cooldown", route.Alias, fmt.Sprintf("all candidates for model route %q are cooling down", route.Alias), fmt.Sprintf("retry after %d seconds", seconds))
}

// fallbackReason describes why the prior target was skipped. Only status and error code are exposed, never bodies.
func fallbackReason(status int, err error) string {
	code := ""
	if err != nil {
		code = codeFromError(err, "")
	}
	if code != "" {
		return fmt.Sprintf("%d %s", status, code)
	}
	return fmt.Sprintf("%d", status)
}
