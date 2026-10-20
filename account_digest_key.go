package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const accountDigestKeyFile = "model-combo-account-digest.key"

// loadOrCreateAccountDigestKey returns the durable HMAC key used for account
// digests. The key lives beside the usage database, is created atomically with
// mode 0600 on first use, and is never logged or exposed. A missing, unreadable,
// or malformed key is an error; callers must fail closed rather than fall back
// to a per-process key, which would make persisted digests unstable.
func loadOrCreateAccountDigestKey(dataPath string) ([]byte, error) {
	dataPath = strings.TrimSpace(dataPath)
	if dataPath == "" {
		return nil, errors.New("account digest key: data_path must not be empty")
	}
	path := filepath.Join(filepath.Dir(filepath.Clean(dataPath)), accountDigestKeyFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("account digest key: create directory: %w", err)
	}
	if key, err := readAccountDigestKey(path); err == nil {
		return key, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("account digest key: generate: %w", err)
	}
	encoded := []byte(hex.EncodeToString(key) + "\n")
	temporary, err := os.CreateTemp(filepath.Dir(path), accountDigestKeyFile+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("account digest key: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return nil, fmt.Errorf("account digest key: chmod temporary file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return nil, fmt.Errorf("account digest key: write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return nil, fmt.Errorf("account digest key: sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, fmt.Errorf("account digest key: close temporary file: %w", err)
	}
	// os.Link fails if the destination exists, so concurrent creators cannot
	// overwrite a key another process already published.
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readAccountDigestKey(path)
		}
		return nil, fmt.Errorf("account digest key: publish: %w", err)
	}
	return key, nil
}

func readAccountDigestKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("account digest key: stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("account digest key: not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("account digest key: permissions %o must not grant group or other access", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("account digest key: read: %w", err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, errors.New("account digest key: corrupt key file")
	}
	return key, nil
}
