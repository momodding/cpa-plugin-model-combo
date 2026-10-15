package main

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// requestCapabilities derives only explicit, protocol-level requirements. An
// omitted capability declaration remains unknown rather than unsupported.
func requestCapabilities(request pluginapi.ExecutorRequest, body executionBody) map[modelCapability]struct{} {
	required := make(map[modelCapability]struct{})
	add := func(capability modelCapability) { required[capability] = struct{}{} }
	if _, _, thinking := splitThinkingSuffix(request.Model); thinking {
		add(modelCapabilityReasoning)
	}
	payload := body.body
	if !gjson.ValidBytes(payload) {
		return required
	}
	if gjson.GetBytes(payload, "tools").IsArray() && len(gjson.GetBytes(payload, "tools").Array()) > 0 ||
		gjson.GetBytes(payload, "functions").IsArray() && len(gjson.GetBytes(payload, "functions").Array()) > 0 {
		add(modelCapabilityTools)
	}
	for _, key := range []string{"reasoning", "thinking", "reasoning_effort", "thinking_effort"} {
		if gjson.GetBytes(payload, key).Exists() {
			add(modelCapabilityReasoning)
			break
		}
	}
	if gjson.GetBytes(payload, "response_format.json_schema.strict").Bool() || gjson.GetBytes(payload, "response_schema.strict").Bool() {
		add(modelCapabilityJSONSchema)
	}
	var scan func(gjson.Result)
	scan = func(value gjson.Result) {
		if value.IsArray() {
			for _, item := range value.Array() {
				scan(item)
			}
			return
		}
		if !value.IsObject() {
			return
		}
		switch strings.ToLower(value.Get("type").String()) {
		case "image", "image_url", "input_image":
			add(modelCapabilityVisionInput)
		case "audio", "input_audio":
			add(modelCapabilityAudioInput)
		case "document", "file", "input_file":
			add(modelCapabilityDocumentInput)
		}
		value.ForEach(func(_ gjson.Result, child gjson.Result) bool {
			scan(child)
			return true
		})
	}
	for _, key := range []string{"messages", "input", "contents"} {
		scan(gjson.GetBytes(payload, key))
	}
	return required
}

func candidateSupports(c candidatePlan, policy capabilityPolicy, required map[modelCapability]struct{}) bool {
	if len(required) == 0 {
		return true
	}
	if !c.capabilitiesDeclared {
		return policy != capabilityPolicyRequireDeclared
	}
	for capability := range required {
		if _, supported := c.capabilities[capability]; !supported {
			return false
		}
	}
	return true
}
