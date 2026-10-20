package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var testAccountDigestKey = []byte("test-account-digest-key")

func TestAccountDigestKeyCreatedWithPrivateModeAndStableAcrossLoads(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "usage.db")
	first, err := loadOrCreateAccountDigestKey(dataPath)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if len(first) != 32 {
		t.Fatalf("key length = %d", len(first))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(filepath.Dir(dataPath), accountDigestKeyFile))
		if err != nil {
			t.Fatalf("stat key: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("key mode = %o, want 600", info.Mode().Perm())
		}
	}
	second, err := loadOrCreateAccountDigestKey(dataPath)
	if err != nil {
		t.Fatalf("reload key: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("key changed across loads")
	}
}

func TestAccountDigestSurvivesTrackerRestart(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	dataPath := filepath.Join(t.TempDir(), "usage.db")
	account := resolvedAccount{key: "pool-a", id: "auth-1"}
	digestFor := func() string {
		key, err := loadOrCreateAccountDigestKey(dataPath)
		if err != nil {
			t.Fatalf("load key: %v", err)
		}
		tracker := newAttributionTracker(func() time.Time { return now })
		tracker.setAccountDigestKey(key)
		tracker.MarkRoutedWithAccount("auto", "gpt-5.4", nil, account)
		result := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", AuthID: "auth-1", RequestedAt: now})
		if result.AccountDigest == "" {
			t.Fatal("missing account digest")
		}
		return result.AccountDigest
	}
	if before, after := digestFor(), digestFor(); before != after {
		t.Fatalf("digest changed across restart: %q != %q", before, after)
	}
}

func TestAccountDigestKeyFailsClosedOnCorruptOrPermissiveFile(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "usage.db")
	keyPath := filepath.Join(filepath.Dir(dataPath), accountDigestKeyFile)
	if err := os.WriteFile(keyPath, []byte("not-hex-secret-value"), 0o600); err != nil {
		t.Fatalf("write corrupt key: %v", err)
	}
	_, err := loadOrCreateAccountDigestKey(dataPath)
	if err == nil || strings.Contains(err.Error(), "not-hex-secret-value") {
		t.Fatalf("corrupt key error = %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(keyPath, []byte(strings.Repeat("ab", 32)+"\n"), 0o644); err != nil {
			t.Fatalf("write key: %v", err)
		}
		if err := os.Chmod(keyPath, 0o644); err != nil {
			t.Fatalf("chmod key: %v", err)
		}
		_, err := loadOrCreateAccountDigestKey(dataPath)
		if err == nil || strings.Contains(err.Error(), strings.Repeat("ab", 32)) {
			t.Fatalf("permissive key error = %v", err)
		}
	}
}

func TestStoredDigestCarriesNoKeyMaterial(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	key, err := loadOrCreateAccountDigestKey(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	tracker := newAttributionTracker(func() time.Time { return now })
	tracker.setAccountDigestKey(key)
	tracker.MarkRoutedWithAccount("auto", "gpt-5.4", nil, resolvedAccount{key: "pool-a", id: "auth-1"})
	result := tracker.Match(pluginapi.UsageRecord{Model: "gpt-5.4", AuthID: "auth-1", RequestedAt: now})
	stored, err := json.Marshal(storedRecordFromUsage(pluginapi.UsageRecord{Model: "gpt-5.4", RequestedAt: now}, result))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(stored), string(key)) || strings.Contains(string(stored), "pool-a") || strings.Contains(string(stored), "auth-1") {
		t.Fatalf("stored record leaks key material or identifiers: %s", stored)
	}
}
