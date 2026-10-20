//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSecureAccountDigestKeyRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("key"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	path := filepath.Join(dir, "key")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := readSecureAccountDigestKey(path); err == nil {
		t.Fatal("read symlink succeeded")
	}
}
