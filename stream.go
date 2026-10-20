package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (p *modelComboPlugin) startStream(ctx context.Context, request executorRPCRequest, host modelHost) ([]byte, error) {
	streamID := strings.TrimSpace(request.StreamID)
	if streamID == "" {
		return errorEnvelope("executor_error", "stream_id is required for executor.execute_stream", 0), nil
	}
	go func() {
		errorMessage := ""
		defer func() {
			if recovered := recover(); recovered != nil {
				errorMessage = fmt.Sprintf("stream orchestration panic: %v", recovered)
			}
			host.ClosePluginStream(streamID, errorMessage)
		}()
		if err := p.executeStreamWithHost(ctx, request.ExecutorRequest, streamID, host); err != nil {
			errorMessage = err.Error()
		}
	}()
	return okEnvelope(map[string]any{"headers": http.Header{"Content-Type": []string{"text/event-stream"}}})
}

func (p *modelComboPlugin) executeStreamWithHost(ctx context.Context, request pluginapi.ExecutorRequest, pluginStreamID string, host modelHost) error {
	if p == nil || host == nil {
		return statusError{status: http.StatusBadGateway, message: "model combo host callback is unavailable"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, ok := p.matchingCombo(request.Model)
	if !ok {
		return statusError{status: http.StatusBadGateway, message: "no model combo matched executor stream request"}
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
				receivedPayload, err := forwardStreamAttemptWithAuth(request, bodyInfo, target, requestedModel, pluginStreamID, host, authID)
				if err == nil && !receivedPayload {
					err = statusError{status: http.StatusBadGateway, message: "host model stream ended without payload"}
				}
				if err == nil {
					p.comboRuntime.MarkSuccess(plan, group, selection.candidate)
					return nil
				}
				if receivedPayload {
					p.comboRuntime.MarkSuccess(plan, group, selection.candidate)
					return err
				}
				failure := classifyAttemptFailure(err, nil, policy, time.Duration(plan.resilience.MaxBackoffMilliseconds)*time.Millisecond, time.Until(ledger.deadline))
				if failure.terminal || !failure.retry {
					p.comboRuntime.ReleaseProbe(plan, group, selection.candidate)
					if pooled && failure.penalize {
						logicalKey := selectedAccount.key + "\x00" + selectedAccount.id
						p.comboRuntime.MarkAccountFailure(selection.candidate.accountPool.signature, logicalKey, failure.retryAfter)
					}
					return err
				}
				lastErr, ledger.lastErr = err, err
				candidatePenalized = candidatePenalized || failure.penalize
				if pooled && failure.penalize {
					logicalKey := selectedAccount.key + "\x00" + selectedAccount.id
					p.comboRuntime.MarkAccountFailure(selection.candidate.accountPool.signature, logicalKey, failure.retryAfter)
				}
				if err := waitForBackoff(ctx, backoffDelay(ledger.count-1, plan.resilience, failure.retryAfter, time.Until(ledger.deadline))); err != nil {
					p.comboRuntime.ReleaseProbe(plan, group, selection.candidate)
					return err
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
		return cooldownComboError(plan.alias, retryAfter)
	}
	detail := ""
	if lastErr != nil {
		detail = lastErr.Error()
	}
	return newRouteError(http.StatusServiceUnavailable, "model_combo_unavailable", plan.alias, fmt.Sprintf("no available candidate for model combo %q", plan.alias), detail)
}

func forwardStreamAttempt(request pluginapi.ExecutorRequest, bodyInfo executionBody, target, requestedModel, pluginStreamID string, host modelHost) (bool, error) {
	return forwardStreamAttemptWithAuth(request, bodyInfo, target, requestedModel, pluginStreamID, host, "")
}

func forwardStreamAttemptWithAuth(request pluginapi.ExecutorRequest, bodyInfo executionBody, target, requestedModel, pluginStreamID string, host modelHost, authID string) (bool, error) {
	response, err := host.StartStream(hostRequestWithAuth(request, bodyInfo, target, true, authID))
	if err != nil {
		// StartStream may report the upstream status alongside the error. Preserve it
		// so the failure is classified by the fallback policy instead of falling
		// through to the ambiguous-error path.
		if status := response.StatusCode; status > 0 {
			return false, statusError{status: status, message: err.Error()}
		}
		return false, err
	}
	if response.StatusCode >= 400 {
		_ = host.CloseStream(response.StreamID)
		return false, statusError{status: response.StatusCode, message: fmt.Sprintf("host model %s stream returned status %d", target, response.StatusCode)}
	}
	if strings.TrimSpace(response.StreamID) == "" {
		return false, statusError{status: http.StatusBadGateway, message: "host model stream returned an empty stream id"}
	}
	defer func() { _ = host.CloseStream(response.StreamID) }()
	rewriter := newStreamModelRewriter(requestedModel)
	receivedPayload := false
	for {
		chunk, err := host.ReadStream(response.StreamID)
		if err != nil {
			return receivedPayload, err
		}
		if strings.TrimSpace(chunk.Error) != "" {
			return receivedPayload, statusError{status: statusFromError(fmt.Errorf("%s", chunk.Error)), message: chunk.Error}
		}
		if len(chunk.Payload) > 0 {
			receivedPayload = true
			if rewritten := rewriter.Rewrite(chunk.Payload); len(rewritten) > 0 {
				if err := host.Emit(pluginStreamID, rewritten); err != nil {
					return true, err
				}
			}
		}
		if chunk.Done {
			if tail := rewriter.Finish(); len(tail) > 0 {
				if err := host.Emit(pluginStreamID, tail); err != nil {
					return receivedPayload, err
				}
			}
			return receivedPayload, nil
		}
	}
}
