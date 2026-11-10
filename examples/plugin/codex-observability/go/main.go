package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
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

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var state = newPluginState()

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	state.shutdown()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errLoad := state.loadConfig(request); errLoad != nil {
			return nil, errLoad
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodRequestInterceptBefore:
		return interceptBeforeAuth(request)
	case pluginabi.MethodRequestInterceptAfter:
		return passThroughRequest(request)
	case pluginabi.MethodUsageHandle:
		if errUsage := state.handleUsage(request); errUsage != nil {
			return nil, errUsage
		}
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	RequestInterceptor bool `json:"request_interceptor"`
	UsagePlugin        bool `json:"usage_plugin"`
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "model-allowlist",
					Type:        pluginapi.ConfigFieldTypeObject,
					Description: "Model gate. Only listed models may be executed; everything else is rejected.",
				},
				{
					Name:        "observe",
					Type:        pluginapi.ConfigFieldTypeObject,
					Description: "Half-hourly usage and upstream quota observation written as NDJSON.",
				},
				{
					Name:        "observe.quota-header-dump",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Also record every x-codex-* response header verbatim for upstream signal discovery.",
				},
			},
		},
		Capabilities: registrationCapability{
			RequestInterceptor: true,
			UsagePlugin:        true,
		},
	}
}

// interceptBeforeAuth enforces the model allowlist.
func interceptBeforeAuth(raw []byte) ([]byte, error) {
	var request pluginapi.RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	gate := state.gateSnapshot()
	if gate.allows(request.RequestedModel, request.Model) {
		return okEnvelope(pluginapi.RequestInterceptResponse{Headers: request.Headers, Body: request.Body})
	}
	name := strings.TrimSpace(request.RequestedModel)
	if name == "" {
		name = strings.TrimSpace(request.Model)
	}
	return rejectModel(gate, name)
}

// passThroughRequest leaves the after-auth stage untouched.
func passThroughRequest(raw []byte) ([]byte, error) {
	var request pluginapi.RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{Headers: request.Headers, Body: request.Body})
}

func rejectModel(gate allowlist, name string) ([]byte, error) {
	body, errMarshal := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("%s: %s", gate.message, name),
			"type":    "invalid_request_error",
			"code":    gate.errorCode,
			"param":   "model",
		},
	})
	if errMarshal != nil {
		return nil, errMarshal
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	return okEnvelope(pluginapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      gate.statusCode,
		ResponseHeaders: headers,
		ResponseBody:    body,
	})
}

// handleUsage feeds one usage record into the observer.
func (s *pluginState) handleUsage(raw []byte) error {
	var record pluginapi.UsageRecord
	if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
		return errUnmarshal
	}
	observer := s.observerSnapshot()
	if observer == nil {
		return nil
	}
	observer.record(record)
	return nil
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, errMarshal := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode error"}}`)
	}
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
