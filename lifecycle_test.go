package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestLifecycleIdentityAndCredentialSnapshots(t *testing.T) {
	store, err := openUsageStore(filepath.Join(t.TempDir(), "usage.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := &modelRouterPlugin{config: routerConfig{Enabled: true}, store: store, credentials: &credentialSnapshots{}}
	p.credentials.replace([]pluginapi.HostAuthFileEntry{{ID: "a", AuthIndex: "one", Label: "Work", Email: "work@example.com"}})
	ctx := context.Background()
	request := pluginapi.RequestInterceptRequest{RequestID: "lifecycle", TraceID: "trace", RequestedModel: "requested", Metadata: map[string]any{"request_path": "/v1/chat/completions"}}
	if _, err := p.InterceptRequestBeforeAuth(ctx, request); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p.HandleUsage(ctx, pluginapi.UsageRecord{RequestID: "execution", TraceID: "trace", AuthID: "a", RequestedAt: now, Failed: true})
	p.credentials.replace(nil)
	if err := p.HandleRequestComplete(ctx, pluginapi.RequestCompletion{RequestID: "nested", TraceID: "trace", StartedAt: now, CompletedAt: now, Outcome: pluginapi.RequestCompletionFailed}); err != nil {
		t.Fatal(err)
	}
	if err := p.HandleRequestComplete(ctx, pluginapi.RequestCompletion{RequestID: "lifecycle", TraceID: "trace", StartedAt: now, CompletedAt: now.Add(time.Second), Outcome: pluginapi.RequestCompletionSucceeded, StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	if got := p.credentials.label("a", "one"); got != "" {
		t.Fatalf("removed credential snapshot = %q", got)
	}
	sessions := readTestSessions(t, store)
	if len(sessions) != 1 || sessions[0].LifecycleRequestID != "lifecycle" || sessions[0].RequestEndpoint != "/v1/chat/completions" || sessions[0].Outcome != "succeeded" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestSafeCredentialSnapshot(t *testing.T) {
	s := &credentialSnapshots{}
	s.replace([]pluginapi.HostAuthFileEntry{
		{ID: "a", AuthIndex: "shared", Label: "sk-secret", Email: "safe@example.com"},
		{ID: "b", AuthIndex: "shared", Label: "Other\nAccount"},
		{ID: "c", AuthIndex: "alone", Label: "eyJhbGciOiJub25lIn0.secret.secret"},
	})
	if got := s.label("a", "shared"); got != "safe@example.com" {
		t.Fatalf("safe email = %q", got)
	}
	if got := s.label("", "shared"); got != "" {
		t.Fatalf("ambiguous index = %q", got)
	}
	if got := s.label("c", "alone"); got != "" {
		t.Fatalf("secret label = %q", got)
	}
	if got := s.label("b", ""); got != "Other Account" {
		t.Fatalf("clean label = %q", got)
	}
}
