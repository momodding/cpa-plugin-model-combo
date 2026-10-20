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

const (
	accountDigestKeyFile = "model-combo-account-digest.key"
	accountDigestKeySize = 32
)

func loadOrCreateAccountDigestKey(dataPath string) ([]byte, error) {
	dataPath = strings.TrimSpace(dataPath)
	if dataPath == "" {
		return nil, errors.New("account digest key: data_path must not be empty")
	}

	path := filepath.Join(filepath.Dir(filepath.Clean(dataPath)), accountDigestKeyFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("account digest key: create directory: %w", err)
	}

	key, err := readAccountDigestKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	key = make([]byte, accountDigestKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("account digest key: generate: %w", err)
	}
	if err := createAccountDigestKey(path, []byte(hex.EncodeToString(key)+"\n")); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readAccountDigestKey(path)
		}
		return nil, err
	}
	return key, nil
}

func readAccountDigestKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("account digest key: stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("account digest key: file must be regular")
	}

	encoded, err := readSecureAccountDigestKey(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(key) != accountDigestKeySize {
		return nil, errors.New("account digest key: corrupt key file")
	}
	return key, nil
}
