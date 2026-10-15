package main

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (p *modelComboPlugin) RouteModel(_ context.Context, request pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
	if p == nil || !p.config.Enabled {
		return pluginapi.ModelRouteResponse{Handled: false}, nil
	}
	if _, ok := lookupCombo(p.plans, request.RequestedModel); !ok {
		return pluginapi.ModelRouteResponse{Handled: false}, nil
	}
	return pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     pluginID + ":matched",
	}, nil
}

func (p *modelComboPlugin) RegisterModels(_ context.Context, _ pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
	response := pluginapi.ModelRegistrationResponse{Provider: pluginID}
	if p == nil || !p.config.Enabled {
		return response, nil
	}
	response.Models = make([]pluginapi.ModelInfo, 0, len(p.plans))
	for _, plan := range p.plans {
		alias := strings.TrimSpace(plan.alias)
		response.Models = append(response.Models, pluginapi.ModelInfo{
			ID:          alias,
			Object:      "model",
			OwnedBy:     pluginID,
			DisplayName: alias,
			Name:        alias,
			Description: "CLIProxyAPI logical model combo",
			UserDefined: true,
		})
	}
	return response, nil
}

func (p *modelComboPlugin) matchingCombo(requestedModel string) (comboPlan, bool) {
	if p == nil {
		return comboPlan{}, false
	}
	return lookupCombo(p.plans, requestedModel)
}
