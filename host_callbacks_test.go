package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCallbackModelHostListAuthUsesEmptyWirePayload(t *testing.T) {
	called := false
	host := callbackModelHost{
		hostCallbackID: "must-not-cross-auth-list-boundary",
		rawCall: func(method string, payload json.RawMessage) (json.RawMessage, error) {
			called = true
			if method != pluginabi.MethodHostAuthList {
				t.Fatalf("method = %q, want %q", method, pluginabi.MethodHostAuthList)
			}
			if string(payload) != "{}" {
				t.Fatalf("payload = %s, want {}", payload)
			}
			if strings.Contains(string(payload), "host_callback_id") {
				t.Fatalf("auth-list payload included callback identifier: %s", payload)
			}
			return json.RawMessage(`{"files":[{"id":"opaque-auth-id","name":"work","provider":"openai"}]}`), nil
		},
	}
	entries, err := host.ListAuth()
	if err != nil {
		t.Fatalf("ListAuth() error = %v", err)
	}
	if !called || len(entries) != 1 || entries[0].ID != "opaque-auth-id" {
		t.Fatalf("ListAuth() = %#v, called = %v", entries, called)
	}
}

func TestCallbackModelHostListAuthRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		payload json.RawMessage
	}{
		{name: "bare array", payload: json.RawMessage(`[{"id":"auth-1","name":"work"}]`)},
		{name: "malformed JSON", payload: json.RawMessage(`{"files":`)},
		{name: "missing files", payload: json.RawMessage(`{}`)},
		{name: "null files", payload: json.RawMessage(`{"files":null}`)},
		{name: "non-array files", payload: json.RawMessage(`{"files":{}}`)},
		{name: "missing auth ID", payload: json.RawMessage(`{"files":[{"name":"work"}]}`)},
		{name: "duplicate auth ID", payload: json.RawMessage(`{"files":[{"id":"auth-1","name":"work"},{"id":"auth-1","name":"other"}]}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := callbackModelHost{rawCall: func(string, json.RawMessage) (json.RawMessage, error) {
				return test.payload, nil
			}}
			if entries, err := host.ListAuth(); err == nil {
				t.Fatalf("ListAuth() = %#v, nil error", entries)
			}
		})
	}
}

func TestCallbackModelHostRoutesEveryCallbackThroughWireSeam(t *testing.T) {
	var methods []string
	host := callbackModelHost{rawCall: func(method string, payload json.RawMessage) (json.RawMessage, error) {
		methods = append(methods, method)
		return json.RawMessage(`{}`), nil
	}}
	if _, err := host.Execute(pluginapi.HostModelExecutionRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.StartStream(pluginapi.HostModelExecutionRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.ReadStream("upstream"); err != nil {
		t.Fatal(err)
	}
	if err := host.CloseStream("upstream"); err != nil {
		t.Fatal(err)
	}
	if err := host.Emit("plugin", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	host.ClosePluginStream("plugin", "closed")
	want := []string{
		pluginabi.MethodHostModelExecute,
		pluginabi.MethodHostModelExecuteStream,
		pluginabi.MethodHostModelStreamRead,
		pluginabi.MethodHostModelStreamClose,
		pluginabi.MethodHostStreamEmit,
		pluginabi.MethodHostStreamClose,
	}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("methods = %#v, want %#v", methods, want)
	}
}
