package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type modelHost interface {
	ListAuth() ([]pluginapi.HostAuthFileEntry, error)
	Execute(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error)
	StartStream(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error)
	ReadStream(string) (pluginapi.HostModelStreamReadResponse, error)
	CloseStream(string) error
	Emit(string, []byte) error
	ClosePluginStream(string, string)
}

func (p *modelComboPlugin) executeWithHost(request pluginapi.ExecutorRequest, host modelHost) (pluginapi.ExecutorResponse, error) {
	return p.executeWithHostContext(context.Background(), request, host)
}

func (p *modelComboPlugin) executeWithHostContext(ctx context.Context, request pluginapi.ExecutorRequest, host modelHost) (pluginapi.ExecutorResponse, error) {
	if p == nil || host == nil {
		return pluginapi.ExecutorResponse{}, statusError{status: http.StatusBadGateway, message: "model combo host callback is unavailable"}
	}
	plan, ok := p.matchingCombo(request.Model)
	if !ok {
		return pluginapi.ExecutorResponse{}, statusError{status: http.StatusBadGateway, message: "no model combo matched executor request"}
	}
	requestedModel := strings.TrimSpace(request.Model)
	bodyInfo := bodyForExecution(request)
	requiredCapabilities := requestCapabilities(request, bodyInfo)
	policy := p.config.fallbackPolicy()
	ledger := newAttemptLedger(plan.maxAttempts, plan.resilience.DeadlineMilliseconds)
	var lastErr error
	var retryAfter time.Duration
	allGroupsHealthBlocked := true
	var authEntries []pluginapi.HostAuthFileEntry
	var authErr error
	authListed := false

	for _, group := range plan.groups {
		for ledger.canAttempt() {
			selection := p.comboRuntime.selectFromGroup(plan, group, ledger, requiredCapabilities)
			if selection.candidate.model == "" {
				if selection.allCooling && selection.retryAfter > retryAfter {
					retryAfter = selection.retryAfter
				} else if !selection.allCooling {
					allGroupsHealthBlocked = false
				}
				break
			}
			allGroupsHealthBlocked = false
			pooled := selection.candidate.accountPool != nil
			resolved := []resolvedAccount{{}}
			if pooled {
				if !authListed {
					authEntries, authErr = host.ListAuth()
					authListed = true
				}
				resolved = resolveAccountPool(selection.candidate.accountPool, authEntries)
				if authErr != nil {
					lastErr = authErr
				}
				if authErr != nil || len(resolved) == 0 {
					ledger.markCandidateExhausted(selection.candidate.candidateKey)
					p.comboRuntime.ReleaseProbe(plan, group, selection.candidate)
					continue
				}
			}
			var selectedAccount resolvedAccount
			attemptedAccounts := make(map[string]struct{}, len(resolved))
			candidatePenalized := false
			for ledger.canAttempt() {
				authID := ""
				if pooled {
					available := excludeResolvedAccounts(filterCooledAccounts(p.comboRuntime, selection.candidate.accountPool.signature, resolved), attemptedAccounts)
					account, ok := selectResolvedAccount(p.comboRuntime, selection.candidate.accountPool, available)
					if !ok {
						break
					}
					selectedAccount = account
					logicalKey := account.key + "\x00" + account.id
					authID = account.id
					attemptedAccounts[logicalKey] = struct{}{}
				}
				ledger.recordAttempt()
				target := targetModel(requestedModel, selection.candidate.model)
				p.attribution.MarkRoutedWithAccount(plan.alias, target, request.Headers, selectedAccount)
				response, err := host.Execute(hostRequestWithAuth(request, bodyInfo, target, false, authID))
				status := response.StatusCode
				if status == 0 && err == nil {
					status = http.StatusOK
				}
				if status == 0 && err != nil {
					status = statusFromError(err)
				}
				if err == nil && status >= http.StatusOK && status < http.StatusMultipleChoices {
					p.comboRuntime.MarkSuccess(plan, group, selection.candidate)
					return pluginapi.ExecutorResponse{Payload: rewriteResponseModel(response.Body, requestedModel), Headers: cloneHeader(response.Headers), Metadata: map[string]any{"combo_alias": plan.alias, "group_name": group.name, "selected_model": target, "selected_index": ledger.count - 1}}, nil
				}
				if err == nil {
					err = statusError{status: status, message: hostStatusMessage(target, status, response.Body)}
				}
				failure := classifyAttemptFailure(err, response.Headers, policy, time.Duration(plan.resilience.MaxBackoffMilliseconds)*time.Millisecond, time.Until(ledger.deadline))
				if failure.terminal || !failure.retry {
					if pooled && selectedAccount.key != "" && failure.penalize {
						logicalKey := selectedAccount.key + "\x00" + selectedAccount.id
						p.comboRuntime.MarkAccountFailure(selection.candidate.accountPool.signature, logicalKey, failure.retryAfter)
					}
					p.comboRuntime.ReleaseProbe(plan, group, selection.candidate)
					return pluginapi.ExecutorResponse{}, err
				}
				lastErr, ledger.lastErr = err, err
				candidatePenalized = candidatePenalized || failure.penalize
				if pooled && selectedAccount.key != "" && failure.penalize {
					logicalKey := selectedAccount.key + "\x00" + selectedAccount.id
					p.comboRuntime.MarkAccountFailure(selection.candidate.accountPool.signature, logicalKey, failure.retryAfter)
				}
				if err := waitForBackoff(ctx, backoffDelay(ledger.count-1, plan.resilience, failure.retryAfter, time.Until(ledger.deadline))); err != nil {
					p.comboRuntime.ReleaseProbe(plan, group, selection.candidate)
					return pluginapi.ExecutorResponse{}, err
				}
				if !pooled {
					break
				}
			}
			ledger.markCandidateExhausted(selection.candidate.candidateKey)
			if candidatePenalized {
				p.comboRuntime.MarkFailure(plan, group, selection.candidate, plan.resilience)
			} else {
				p.comboRuntime.ReleaseProbe(plan, group, selection.candidate)
			}
		}
	}
	if lastErr == nil && allGroupsHealthBlocked && retryAfter > 0 {
		return pluginapi.ExecutorResponse{}, cooldownComboError(plan.alias, retryAfter)
	}
	detail := ""
	if lastErr != nil {
		detail = lastErr.Error()
	}
	return pluginapi.ExecutorResponse{}, newRouteError(http.StatusServiceUnavailable, "model_combo_unavailable", plan.alias, fmt.Sprintf("no available candidate for model combo %q", plan.alias), detail)
}

func hostRequest(request pluginapi.ExecutorRequest, bodyInfo executionBody, model string, stream bool) pluginapi.HostModelExecutionRequest {
	return hostRequestWithAuth(request, bodyInfo, model, stream, "")
}

func hostRequestWithAuth(request pluginapi.ExecutorRequest, bodyInfo executionBody, model string, stream bool, authID string) pluginapi.HostModelExecutionRequest {
	return pluginapi.HostModelExecutionRequest{
		EntryProtocol: bodyInfo.entryProtocol,
		ExitProtocol:  bodyInfo.responseProtocol,
		Model:         model,
		Stream:        stream,
		AuthID:        authID,
		Body:          requestBodyForTarget(bodyInfo.body, model),
		Headers:       sanitizeNestedHeaders(request.Headers),
		Query:         cloneValues(request.Query),
		Alt:           request.Alt,
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

func cooldownComboError(alias string, retryAfter time.Duration) error {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return newRouteError(http.StatusTooManyRequests, "model_combo_cooldown", alias, fmt.Sprintf("all candidates for model combo %q are cooling down", alias), fmt.Sprintf("retry after %d seconds", seconds))
}
