package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type headroomCandidate struct {
	model   string
	account accountCredential
	score   float64
	state   quotaState
}

// headroomCandidates filters cooldowns before ranking and preserves target/account order on ties.
func (p *modelRouterPlugin) headroomCandidates(request pluginapi.ExecutorRequest, host modelHost, route modelRoute, accounts map[string][]accountCredential, unavailable map[string]struct{}) ([]headroomCandidate, routeSelection) {
	excluded := make(map[string]struct{}, len(unavailable))
	for key := range unavailable {
		excluded[key] = struct{}{}
	}
	var ordered []string
	var cooling routeSelection
	for {
		selection := p.runtime.SelectExcluding(route, excluded)
		if selection.model == "" {
			cooling = selection
			break
		}
		ordered = append(ordered, selection.model)
		excluded[routeKey(selection.model)] = struct{}{}
	}
	var credentials []accountCredential
	for _, model := range ordered {
		credentials = append(credentials, accounts[routeKey(model)]...)
	}
	var snapshots map[string]quotaSnapshot
	if p.quota != nil {
		snapshots = p.quota.Get(credentials)
	}
	var candidates []headroomCandidate
	for _, model := range ordered {
		for _, account := range accounts[routeKey(model)] {
			score, state := headroom(snapshots[quotaKey(account)], targetModel(request.Model, model))
			if state == quotaExhausted {
				continue
			}
			if state == quotaUnknown {
				score = p.config.QuotaPolling.DefaultUnknownQuotaPercent / 100
			}
			candidates = append(candidates, headroomCandidate{model, account, score, state})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].state != quotaUnlimited && candidates[j].state == quotaUnlimited
	})
	if lookup, ok := host.(interface {
		AffinityLookup(pluginapi.HostAffinityLookupRequest) (pluginapi.HostAffinityLookupResponse, error)
	}); ok {
		session, _ := request.Metadata["canonical_session_id"].(string)
		if session == "" {
			session, _ = request.Metadata["execution_session_id"].(string)
		}
		if session != "" {
			for i, candidate := range candidates {
				response, err := lookup.AffinityLookup(pluginapi.HostAffinityLookupRequest{Provider: candidate.account.Provider, Model: targetModel(request.Model, candidate.model), SessionID: session})
				if err == nil && response.Status == pluginapi.HostAffinityStatusBound && !response.Disabled && !response.Unavailable && response.AuthIndex != "" && response.AuthIndex == candidate.account.AuthIndex {
					copy(candidates[1:i+1], candidates[:i])
					candidates[0] = candidate
					break
				}
			}
		}
	}
	return candidates, cooling
}

func quotaStateName(state quotaState) string { return state.String() }

func (p *modelRouterPlugin) executeHeadroom(request pluginapi.ExecutorRequest, host modelHost, route modelRoute, body executionBody, requestID string, accounts map[string][]accountCredential, unavailable map[string]struct{}) (pluginapi.ExecutorResponse, error) {
	candidates, cooling := p.headroomCandidates(request, host, route, accounts, unavailable)
	if len(candidates) == 0 && cooling.allCooling {
		return pluginapi.ExecutorResponse{}, cooldownRouteError(route, cooling.retryAfter)
	}
	limit := p.config.attemptLimit(len(route.Targets))
	attempted := map[string]int{}
	var lastErr error
	var lastTarget string
	lastStatus := 0
	for i, candidate := range candidates {
		key := routeKey(candidate.model)
		attempt, exists := attempted[key]
		if !exists {
			if len(attempted) >= limit {
				continue
			}
			attempt = len(attempted)
			attempted[key] = attempt
		}
		target := targetModel(request.Model, candidate.model)
		p.attribution.MarkRouted(route.Alias, target, request.Headers, requestID, attempt)
		response, err := host.Execute(hostRequest(request, body, target, false, candidate.account))
		status := response.StatusCode
		if status == 0 && err == nil {
			status = http.StatusOK
		}
		if status == 0 && err != nil {
			status = statusFromError(err)
		}
		if err == nil && status >= 200 && status < 300 {
			p.runtime.MarkSuccess(route, candidate.model)
			if p.quota != nil {
				p.quota.RecordSelection(route.Alias, target, candidate.account, candidate.score, candidate.state, "")
			}
			metadata := map[string]any{"route_alias": route.Alias, "selected_model": target, "selected_index": attempt, "headroom_score": candidate.score, "quota_state": quotaStateName(candidate.state)}
			if lastTarget != "" {
				metadata["fallback_attempt"] = attempt
				metadata["fallback_from"] = lastTarget
				metadata["fallback_reason"] = fallbackReason(lastStatus, lastErr)
			}
			return pluginapi.ExecutorResponse{Payload: rewriteResponseModel(response.Body, request.Model), Headers: cloneHeader(response.Headers), Metadata: metadata}, nil
		}
		if err == nil {
			err = statusError{status: status, message: hostStatusMessage(target, status, response.Body)}
		}
		if terminalRequestError(err) {
			return pluginapi.ExecutorResponse{}, safeRestrictedFailure(err)
		}
		retry := eligibleRouteFailure(err, p.config.fallbackPolicy())
		err = safeRestrictedFailure(err)
		if !retry || p.config.FallbackDisabled {
			return pluginapi.ExecutorResponse{}, err
		}
		lastErr, lastTarget, lastStatus = err, target, status
		if !hasLaterModel(candidates, i, key) {
			p.runtime.MarkFailure(route, candidate.model)
		}
	}
	detail := ""
	if lastErr != nil {
		detail = lastErr.Error()
	}
	return pluginapi.ExecutorResponse{}, newRouteError(http.StatusServiceUnavailable, "model_route_unavailable", route.Alias, fmt.Sprintf("no available candidate for model route %q", route.Alias), detail)
}

func hasLaterModel(candidates []headroomCandidate, index int, key string) bool {
	for _, candidate := range candidates[index+1:] {
		if routeKey(candidate.model) == key {
			return true
		}
	}
	return false
}

func (p *modelRouterPlugin) streamHeadroom(ctx context.Context, request pluginapi.ExecutorRequest, streamID string, host modelHost, onPayload func(), route modelRoute, body executionBody, requestID string, accounts map[string][]accountCredential, unavailable map[string]struct{}) error {
	candidates, cooling := p.headroomCandidates(request, host, route, accounts, unavailable)
	if len(candidates) == 0 && cooling.allCooling {
		return cooldownRouteError(route, cooling.retryAfter)
	}
	limit := p.config.attemptLimit(len(route.Targets))
	attempted := map[string]int{}
	var lastErr error
	for i, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := routeKey(candidate.model)
		attempt, exists := attempted[key]
		if !exists {
			if len(attempted) >= limit {
				continue
			}
			attempt = len(attempted)
			attempted[key] = attempt
		}
		target := targetModel(request.Model, candidate.model)
		p.attribution.MarkRouted(route.Alias, target, request.Headers, requestID, attempt)
		received, err := forwardStreamAttempt(ctx, request, body, target, strings.TrimSpace(request.Model), streamID, host, onPayload, candidate.account)
		if err == nil {
			p.runtime.MarkSuccess(route, candidate.model)
			if p.quota != nil {
				p.quota.RecordSelection(route.Alias, target, candidate.account, candidate.score, candidate.state, "")
			}
			return nil
		}
		if cancelled := ctx.Err(); cancelled != nil {
			return cancelled
		}
		retry := eligibleRouteFailure(err, p.config.fallbackPolicy())
		err = safeRestrictedFailure(err)
		if retry && !received && !hasLaterModel(candidates, i, key) {
			p.runtime.MarkFailure(route, candidate.model)
		}
		if received || !retry || p.config.FallbackDisabled {
			return err
		}
		lastErr = err
	}
	detail := ""
	if lastErr != nil {
		detail = lastErr.Error()
	}
	return newRouteError(http.StatusServiceUnavailable, "model_route_unavailable", route.Alias, fmt.Sprintf("no available candidate for model route %q", route.Alias), detail)
}
