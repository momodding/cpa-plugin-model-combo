package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type hostRawCaller func(method string, payload json.RawMessage) (json.RawMessage, error)

type callbackModelHost struct {
	hostCallbackID string
	rawCall        hostRawCaller
}

type hostAuthListRPC struct {
	Files *json.RawMessage `json:"files"`
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

func (host callbackModelHost) call(method string, payload any) (json.RawMessage, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, err)
	}
	if host.rawCall != nil {
		return host.rawCall(method, rawPayload)
	}
	return callHostRaw(method, rawPayload)
}

func (host callbackModelHost) ListAuth() ([]pluginapi.HostAuthFileEntry, error) {
	result, err := host.call(pluginabi.MethodHostAuthList, struct{}{})
	if err != nil {
		return nil, err
	}
	var response hostAuthListRPC
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, fmt.Errorf("decode host.auth.list response: %w", err)
	}
	if response.Files == nil || strings.TrimSpace(string(*response.Files)) == "null" {
		return nil, fmt.Errorf("decode host.auth.list response: missing files")
	}
	var entries []pluginapi.HostAuthFileEntry
	if err := json.Unmarshal(*response.Files, &entries); err != nil {
		return nil, fmt.Errorf("decode host.auth.list response files: %w", err)
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.ID == "" || strings.TrimSpace(entry.ID) != entry.ID {
			return nil, fmt.Errorf("decode host.auth.list response: missing auth id")
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return nil, fmt.Errorf("decode host.auth.list response: duplicate auth id %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
	}
	return entries, nil
}

func (host callbackModelHost) Execute(request pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
	result, err := host.call(pluginabi.MethodHostModelExecute, hostModelExecutionRPC{HostModelExecutionRequest: request, HostCallbackID: host.hostCallbackID})
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
	result, err := host.call(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRPC{HostModelExecutionRequest: request, HostCallbackID: host.hostCallbackID})
	if err != nil {
		return pluginapi.HostModelStreamResponse{}, err
	}
	var response pluginapi.HostModelStreamResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return pluginapi.HostModelStreamResponse{}, fmt.Errorf("decode host.model.execute_stream response: %w", err)
	}
	return response, nil
}

func (host callbackModelHost) ReadStream(streamID string) (pluginapi.HostModelStreamReadResponse, error) {
	result, err := host.call(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: streamID})
	if err != nil {
		return pluginapi.HostModelStreamReadResponse{}, err
	}
	var response pluginapi.HostModelStreamReadResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return pluginapi.HostModelStreamReadResponse{}, fmt.Errorf("decode host.model.stream_read response: %w", err)
	}
	return response, nil
}

func (host callbackModelHost) CloseStream(streamID string) error {
	if strings.TrimSpace(streamID) == "" {
		return nil
	}
	_, err := host.call(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return err
}

func (host callbackModelHost) Emit(streamID string, payload []byte) error {
	_, err := host.call(pluginabi.MethodHostStreamEmit, streamEmitRPC{StreamID: streamID, Payload: payload})
	return err
}

func (host callbackModelHost) ClosePluginStream(streamID, errorMessage string) {
	_, _ = host.call(pluginabi.MethodHostStreamClose, streamCloseRPC{StreamID: streamID, Error: strings.TrimSpace(errorMessage)})
}
