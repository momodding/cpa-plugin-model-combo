package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type callbackModelHost struct {
	hostCallbackID string
}

type hostModelExecutionRPC struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type streamEmitRPC struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type streamCloseRPC struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func (host callbackModelHost) Execute(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
	result, err := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRPC{HostModelExecutionRequest: request, HostCallbackID: host.hostCallbackID})
	if err != nil {
		return pluginapi.HostModelExecutionResponse{}, err
	}
	var response pluginapi.HostModelExecutionResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return pluginapi.HostModelExecutionResponse{}, fmt.Errorf("decode host.model.execute response: %w", err)
	}
	return response, nil
}

func (host callbackModelHost) StartStream(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelStreamResponse, error) {
	result, err := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRPC{HostModelExecutionRequest: request, HostCallbackID: host.hostCallbackID})
	if err != nil {
		return pluginapi.HostModelStreamResponse{}, err
	}
	var response pluginapi.HostModelStreamResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return pluginapi.HostModelStreamResponse{}, fmt.Errorf("decode host.model.execute_stream response: %w", err)
	}
	return response, nil
}

func (host callbackModelHost) AuthModels() ([]pluginapi.HostAuthModels, error) {
	result, err := callHost(pluginabi.MethodHostAuthModels, struct {
		pluginapi.HostAuthModelsRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}{HostCallbackID: host.hostCallbackID})
	if err != nil {
		return nil, err
	}
	var response pluginapi.HostAuthModelsResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, fmt.Errorf("decode host.auth.models response: %w", err)
	}
	return response.Credentials, nil
}

func (host callbackModelHost) AuthList() ([]pluginapi.HostAuthFileEntry, error) {
	result, err := callHost(pluginabi.MethodHostAuthList, struct {
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}{HostCallbackID: host.hostCallbackID})
	if err != nil {
		return nil, err
	}
	var response struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, fmt.Errorf("decode host.auth.list response: %w", err)
	}
	return response.Files, nil
}

func (host callbackModelHost) AffinityLookup(request pluginapi.HostAffinityLookupRequest) (pluginapi.HostAffinityLookupResponse, error) {
	result, err := callHost(pluginabi.MethodHostAffinityLookup, struct {
		pluginapi.HostAffinityLookupRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}{request, host.hostCallbackID})
	if err != nil {
		return pluginapi.HostAffinityLookupResponse{}, err
	}
	var response pluginapi.HostAffinityLookupResponse
	err = json.Unmarshal(result, &response)
	return response, err
}

func (callbackModelHost) ReadStream(streamID string) (pluginapi.HostModelStreamReadResponse, error) {
	result, err := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: streamID})
	if err != nil {
		return pluginapi.HostModelStreamReadResponse{}, err
	}
	var response pluginapi.HostModelStreamReadResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return pluginapi.HostModelStreamReadResponse{}, fmt.Errorf("decode host.model.stream_read response: %w", err)
	}
	return response, nil
}

func (callbackModelHost) CloseStream(streamID string) error {
	if strings.TrimSpace(streamID) == "" {
		return nil
	}
	_, err := callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return err
}

func (callbackModelHost) Emit(streamID string, payload []byte) error {
	_, err := callHost(pluginabi.MethodHostStreamEmit, streamEmitRPC{StreamID: streamID, Payload: payload})
	return err
}

func (callbackModelHost) ClosePluginStream(streamID, errorMessage string) {
	_, _ = callHost(pluginabi.MethodHostStreamClose, streamCloseRPC{StreamID: streamID, Error: strings.TrimSpace(errorMessage)})
}
