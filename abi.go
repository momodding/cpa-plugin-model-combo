package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int ModelComboPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void ModelComboPluginFree(void*, size_t);
extern void ModelComboPluginShutdown(void);

static const cliproxy_host_api* model_combo_host;

static void model_combo_store_host(const cliproxy_host_api* host) {
	model_combo_host = host;
}

static int model_combo_call_host(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (model_combo_host == NULL || model_combo_host->call == NULL) {
		return 1;
	}
	return model_combo_host->call(model_combo_host->host_ctx, method, request, request_len, response);
}

static void model_combo_free_host_buffer(void* ptr, size_t len) {
	if (model_combo_host != NULL && model_combo_host->free_buffer != NULL && ptr != NULL) {
		model_combo_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type abiEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *abiError       `json:"error,omitempty"`
}

type abiError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type executorRPCRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type modelRouteRPCRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  abiCapabilities    `json:"capabilities"`
}

type abiCapabilities struct {
	ModelRegistrar        bool                         `json:"model_registrar"`
	ModelRouter           bool                         `json:"model_router"`
	Executor              bool                         `json:"executor"`
	RequestInterceptor    bool                         `json:"request_interceptor"`
	RequestLifecycle      bool                         `json:"request_lifecycle_plugin"`
	ResponseInterceptor   bool                         `json:"response_interceptor"`
	StreamInterceptor     bool                         `json:"response_stream_interceptor"`
	UsagePlugin           bool                         `json:"usage_plugin"`
	ManagementAPI         bool                         `json:"management_api"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
}

var modelComboABIState = struct {
	sync.Mutex
	plugin       *modelComboPlugin
	metadata     pluginapi.Metadata
	shuttingDown bool
	inFlight     sync.WaitGroup
}{}

const maxCGoRequestLen = C.size_t(1<<31 - 1)

const registrationSchemaVersion uint32 = 2
const streamBodyOmissionSchemaVersion uint32 = 3

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil {
		return 1
	}
	C.model_combo_store_host(host)
	modelComboABIState.Lock()
	modelComboABIState.shuttingDown = false
	modelComboABIState.Unlock()
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.ModelComboPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.ModelComboPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.ModelComboPluginShutdown)
	return 0
}

//export ModelComboPluginCall
func ModelComboPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeABIResponse(response, errorEnvelope("invalid_method", "method is required", 0))
		return 0
	}
	if requestLen > maxCGoRequestLen {
		writeABIResponse(response, errorEnvelope("request_too_large", "request payload is too large", 0))
		return 0
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleModelComboABIMethod(context.Background(), C.GoString(method), requestBytes)
	if err != nil {
		raw = errorForExecution(err, "plugin_error")
	}
	writeABIResponse(response, raw)
	return 0
}

//export ModelComboPluginFree
func ModelComboPluginFree(pointer unsafe.Pointer, _ C.size_t) {
	if pointer != nil {
		C.free(pointer)
	}
}

//export ModelComboPluginShutdown
func ModelComboPluginShutdown() {
	modelComboABIState.Lock()
	modelComboABIState.shuttingDown = true
	plugin := modelComboABIState.plugin
	modelComboABIState.plugin = nil
	modelComboABIState.metadata = pluginapi.Metadata{}
	modelComboABIState.Unlock()
	modelComboABIState.inFlight.Wait()
	if plugin != nil {
		_ = plugin.Shutdown()
	}
	C.model_combo_store_host(nil)
}

func handleModelComboABIMethod(ctx context.Context, method string, request []byte) ([]byte, error) {
	if method == pluginabi.MethodPluginRegister || method == pluginabi.MethodPluginReconfigure {
		return registerModelCombo(request)
	}
	switch method {
	case pluginabi.MethodManagementRegister:
		return okEnvelope(modelComboManagementRegistration())
	case pluginabi.MethodManagementHandle:
		var rpcRequest managementRPCRequest
		if err := json.Unmarshal(request, &rpcRequest); err != nil {
			return nil, fmt.Errorf("decode management.handle request: %w", err)
		}
		path := strings.TrimRight(strings.TrimSpace(rpcRequest.Path), "/")
		if !strings.HasPrefix(path, modelComboUsageBasePath+"/") {
			return okEnvelope(handleModelComboManagement(nil, rpcRequest.ManagementRequest))
		}
	}
	plugin, metadata, done, err := beginModelComboCall()
	if err != nil {
		return nil, err
	}
	defer done()
	switch method {
	case pluginabi.MethodManagementHandle:
		var rpcRequest managementRPCRequest
		if err := json.Unmarshal(request, &rpcRequest); err != nil {
			return nil, fmt.Errorf("decode management.handle request: %w", err)
		}
		return okEnvelope(handleModelComboManagement(plugin, rpcRequest.ManagementRequest))
	case pluginabi.MethodModelRegister:
		response, err := plugin.RegisterModels(ctx, pluginapi.ModelRegistrationRequest{Plugin: metadata})
		return okEnvelopeWithError(response, err)
	case pluginabi.MethodModelRoute:
		var rpcRequest modelRouteRPCRequest
		if err := json.Unmarshal(request, &rpcRequest); err != nil {
			return nil, fmt.Errorf("decode model.route request: %w", err)
		}
		response, err := plugin.RouteModel(ctx, rpcRequest.ModelRouteRequest)
		return okEnvelopeWithError(response, err)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case pluginabi.MethodExecutorExecute:
		var rpcRequest executorRPCRequest
		if err := json.Unmarshal(request, &rpcRequest); err != nil {
			return nil, fmt.Errorf("decode executor.execute request: %w", err)
		}
		response, err := plugin.executeWithHostContext(ctx, rpcRequest.ExecutorRequest, callbackModelHost{hostCallbackID: rpcRequest.HostCallbackID})
		if err != nil {
			return errorForExecution(err, "executor_error"), nil
		}
		return okEnvelope(response)
	case pluginabi.MethodExecutorExecuteStream:
		var rpcRequest executorRPCRequest
		if err := json.Unmarshal(request, &rpcRequest); err != nil {
			return nil, fmt.Errorf("decode executor.execute_stream request: %w", err)
		}
		return plugin.startStream(ctx, rpcRequest, callbackModelHost{hostCallbackID: rpcRequest.HostCallbackID})
	case pluginabi.MethodExecutorCountTokens:
		_, err := plugin.CountTokens(ctx, pluginapi.ExecutorRequest{})
		return errorForExecution(err, "model_route_count_tokens_unsupported"), nil
	case pluginabi.MethodExecutorHTTPRequest:
		return errorEnvelope("unsupported_method", "executor.http_request is not supported by model-combo", 0), nil
	case pluginabi.MethodRequestInterceptBefore, pluginabi.MethodRequestInterceptAfter:
		var rpcRequest pluginapi.RequestInterceptRequest
		if err := json.Unmarshal(request, &rpcRequest); err != nil {
			return nil, fmt.Errorf("decode %s request: %w", method, err)
		}
		var response pluginapi.RequestInterceptResponse
		var err error
		if method == pluginabi.MethodRequestInterceptBefore {
			response, err = plugin.InterceptRequestBeforeAuth(ctx, rpcRequest)
		} else {
			response, err = plugin.InterceptRequestAfterAuth(ctx, rpcRequest)
		}
		return okEnvelopeWithError(response, err)
	case pluginabi.MethodUsageHandle:
		var record pluginapi.UsageRecord
		if err := json.Unmarshal(request, &record); err != nil {
			return nil, fmt.Errorf("decode usage.handle request: %w", err)
		}
		plugin.HandleUsage(ctx, record)
		return okEnvelope(map[string]any{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, 0), nil
	}
}

func registerModelCombo(raw []byte) ([]byte, error) {
	var request lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, fmt.Errorf("decode lifecycle request: %w", err)
		}
	}
	modelComboABIState.Lock()
	var previous *modelComboPlugin
	if modelComboABIState.plugin != nil {
		previous = modelComboABIState.plugin
	}
	modelComboABIState.Unlock()
	plugin, metadata, err := newModelComboPlugin(request.ConfigYAML, previous)
	if err != nil {
		return nil, err
	}
	modelComboABIState.Lock()
	modelComboABIState.plugin = plugin
	modelComboABIState.metadata = metadata
	modelComboABIState.shuttingDown = false
	modelComboABIState.Unlock()
	formats := []string{"openai", "openai-response", "claude", "gemini", "codex", "antigravity", "interactions"}
	schemaVersion := registrationSchemaVersion
	if request.SchemaVersion >= streamBodyOmissionSchemaVersion {
		schemaVersion = streamBodyOmissionSchemaVersion
	}
	return okEnvelope(abiRegistration{
		SchemaVersion: schemaVersion,
		Metadata:      metadata,
		Capabilities: abiCapabilities{
			ModelRegistrar:        true,
			ModelRouter:           true,
			Executor:              true,
			RequestInterceptor:    true,
			RequestLifecycle:      false,
			ResponseInterceptor:   false,
			StreamInterceptor:     false,
			UsagePlugin:           true,
			ManagementAPI:         true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeStatic,
			ExecutorInputFormats:  formats,
			ExecutorOutputFormats: formats,
		},
	})
}

func beginModelComboCall() (*modelComboPlugin, pluginapi.Metadata, func(), error) {
	modelComboABIState.Lock()
	defer modelComboABIState.Unlock()
	if modelComboABIState.shuttingDown {
		return nil, pluginapi.Metadata{}, nil, fmt.Errorf("%s is shutting down", pluginID)
	}
	if modelComboABIState.plugin == nil {
		return nil, pluginapi.Metadata{}, nil, fmt.Errorf("%s is not registered", pluginID)
	}
	modelComboABIState.inFlight.Add(1)
	return modelComboABIState.plugin, modelComboABIState.metadata, modelComboABIState.inFlight.Done, nil
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, err)
	}
	return callHostRaw(method, rawPayload)
}

func callHostRaw(method string, rawPayload json.RawMessage) (json.RawMessage, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var requestPointer *C.uint8_t
	if len(rawPayload) > 0 {
		pointer := C.CBytes(rawPayload)
		if pointer == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(pointer)
		requestPointer = (*C.uint8_t)(pointer)
	}
	var response C.cliproxy_buffer
	callCode := C.model_combo_call_host(cMethod, requestPointer, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.model_combo_free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}
	var envelope abiEnvelope
	if err := json.Unmarshal(rawResponse, &envelope); err != nil {
		return nil, fmt.Errorf("decode host callback %s envelope: %w", method, err)
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return nil, fmt.Errorf("host callback %s failed", method)
		}
		return nil, statusError{status: envelope.Error.HTTPStatus, code: envelope.Error.Code, message: strings.TrimSpace(envelope.Error.Message)}
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), envelope.Result...), nil
}

func okEnvelopeWithError(value any, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return okEnvelope(value)
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(abiEnvelope{OK: true, Result: raw})
}

func errorForExecution(err error, fallbackCode string) []byte {
	if err == nil {
		return errorEnvelope(fallbackCode, "model combo execution failed", 0)
	}
	return errorEnvelope(codeFromError(err, fallbackCode), err.Error(), statusFromError(err))
}

func errorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(abiEnvelope{OK: false, Error: &abiError{Code: code, Message: message, HTTPStatus: status}})
	return raw
}

func writeABIResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	pointer := C.CBytes(raw)
	if pointer == nil {
		return
	}
	response.ptr = pointer
	response.len = C.size_t(len(raw))
}
