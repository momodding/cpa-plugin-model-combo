package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (p *modelRouterPlugin) startStream(request executorRPCRequest, host modelHost) ([]byte, error) {
	streamID := strings.TrimSpace(request.StreamID)
	if streamID == "" {
		return errorEnvelope("executor_error", "stream_id is required for executor.execute_stream", 0), nil
	}
	startup := make(chan error, 1)
	go func() {
		started := false
		notifyStartup := func(err error) {
			if !started {
				started = true
				startup <- err
			}
		}
		var streamErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				streamErr = fmt.Errorf("stream orchestration panic: %v", recovered)
			}
			notifyStartup(streamErr)
			errorMessage := ""
			if streamErr != nil {
				errorMessage = streamErr.Error()
			}
			if host != nil {
				host.ClosePluginStream(streamID, errorMessage)
			}
		}()
		streamErr = p.executeStreamWithHost(context.Background(), request.ExecutorRequest, streamID, host, func() { notifyStartup(nil) })
	}()
	if err := <-startup; err != nil {
		return errorForExecution(err, "executor_error"), nil
	}
	return okEnvelope(map[string]any{"headers": http.Header{"Content-Type": []string{"text/event-stream"}}})
}

func (p *modelRouterPlugin) executeStreamWithHost(ctx context.Context, request pluginapi.ExecutorRequest, pluginStreamID string, host modelHost, onPayload func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || host == nil {
		return statusError{status: http.StatusBadGateway, message: "model router host callback is unavailable"}
	}
	route, ok := p.matchingRoute(request.Model)
	if !ok {
		return statusError{status: http.StatusBadGateway, message: "no model route matched executor stream request"}
	}
	requestedModel := strings.TrimSpace(request.Model)
	bodyInfo := bodyForExecution(request)
	routerRequestID := rand.Text()
	policy := p.config.fallbackPolicy()
	accounts, unavailable, err := p.routeAccounts(route, requestedModel, host)
	if err != nil {
		return err
	}
	if route.Strategy == routeStrategyHeadroom {
		return p.streamHeadroom(ctx, request, pluginStreamID, host, onPayload, route, bodyInfo, routerRequestID, accounts, unavailable)
	}
	var lastErr error
	attempted := make(map[string]struct{}, len(route.Targets))
	for key := range unavailable {
		attempted[key] = struct{}{}
	}
	limit := p.config.attemptLimit(len(route.Targets))
	for attempt := 0; attempt < limit; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		selection := p.runtime.SelectExcluding(route, attempted)
		if selection.allCooling {
			return cooldownRouteError(route, selection.retryAfter)
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
		for index, account := range candidates {
			p.attribution.MarkRouted(route.Alias, target, request.Headers, routerRequestID, attempt)
			receivedPayload, err := forwardStreamAttempt(ctx, request, bodyInfo, target, requestedModel, pluginStreamID, host, onPayload, account)
			if err == nil {
				p.runtime.MarkSuccess(route, selection.model)
				return nil
			}
			if cancelled := ctx.Err(); cancelled != nil {
				return cancelled
			}
			retry := eligibleRouteFailure(err, policy)
			if account.ID != "" {
				err = safeRestrictedFailure(err)
			}
			if retry && (len(route.AllowedAccounts) == 0 || (!receivedPayload && index == len(candidates)-1)) {
				p.runtime.MarkFailure(route, selection.model)
			}
			if receivedPayload || !retry || p.config.FallbackDisabled {
				return err
			}
			lastErr = err
		}
	}
	detail := ""
	if lastErr != nil {
		detail = lastErr.Error()
	}
	return newRouteError(http.StatusServiceUnavailable, "model_route_unavailable", route.Alias, fmt.Sprintf("no available candidate for model route %q", route.Alias), detail)
}

func forwardStreamAttempt(ctx context.Context, request pluginapi.ExecutorRequest, bodyInfo executionBody, target, requestedModel, pluginStreamID string, host modelHost, onPayload func(), account accountCredential) (bool, error) {
	response, err := host.StartStream(hostRequest(request, bodyInfo, target, true, account))
	if response.StreamID != "" {
		closeUpstream := sync.OnceFunc(func() { _ = host.CloseStream(response.StreamID) })
		stopCancellation := context.AfterFunc(ctx, closeUpstream)
		defer func() { stopCancellation(); closeUpstream() }()
	}
	if cancelled := ctx.Err(); cancelled != nil {
		return false, cancelled
	}
	if err != nil {
		if terminalRequestError(err) {
			return false, err
		}
		// StartStream may report the upstream status alongside the error. Preserve it
		// so the failure is classified by the fallback policy instead of falling
		// through to the ambiguous-error path.
		if status := response.StatusCode; status > 0 {
			return false, statusError{status: status, message: err.Error()}
		}
		return false, err
	}
	if response.StatusCode >= 400 {
		return false, statusError{status: response.StatusCode, message: fmt.Sprintf("host model %s stream returned status %d", target, response.StatusCode)}
	}
	if strings.TrimSpace(response.StreamID) == "" {
		return false, statusError{status: http.StatusBadGateway, message: "host model stream returned an empty stream id"}
	}
	rewriter := newStreamModelRewriter(requestedModel)
	receivedPayload := false
	for {
		chunk, err := host.ReadStream(response.StreamID)
		if cancelled := ctx.Err(); cancelled != nil {
			return receivedPayload, cancelled
		}
		if err != nil {
			return receivedPayload, err
		}
		if strings.TrimSpace(chunk.Error) != "" {
			return receivedPayload, statusError{status: statusFromError(fmt.Errorf("%s", chunk.Error)), message: chunk.Error}
		}
		if len(chunk.Payload) > 0 {
			if !receivedPayload && onPayload != nil {
				onPayload()
			}
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
