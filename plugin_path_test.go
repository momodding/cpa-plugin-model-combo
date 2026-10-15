package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDefaultDataPath(t *testing.T) {
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(plugins, defaultDataFileName)
	if got := resolveDefaultDataPath(filepath.Join(plugins, "linux", "amd64", "model-combo.so"), "", ""); got != want {
		t.Fatalf("plugin path default = %q, want %q", got, want)
	}
	if got := resolveDefaultDataPath("", filepath.Join(root, "CLIProxyAPI"), ""); got != want {
		t.Fatalf("executable path default = %q, want %q", got, want)
	}
	if got := resolveDefaultDataPath("", "", root); got != want {
		t.Fatalf("working directory default = %q, want %q", got, want)
	}
}

func TestResolveDefaultDataPathFallsBackOutsideCPALayout(t *testing.T) {
	workingDir := t.TempDir()
	want := filepath.Join(workingDir, "plugins", defaultDataFileName)
	if got := resolveDefaultDataPath("/model-combo.so", "/CLIProxyAPI", workingDir); got != want {
		t.Fatalf("fallback = %q, want %q", got, want)
	}
}

func TestResolveDefaultDataPathDoesNotSelectOtherDatabase(t *testing.T) {
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "data", "other-usage.db")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(plugins, defaultDataFileName)
	if got := resolveDefaultDataPath(filepath.Join(plugins, "model-combo.so"), "", root); got != want {
		t.Fatalf("default with other database = %q, want %q", got, want)
	}
}
