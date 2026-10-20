//go:build !windows

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func readSecureAccountDigestKey(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("account digest key: stat: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		syscall.Close(fd)
		return nil, errors.New("account digest key: open validated handle")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("account digest key: stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("account digest key: file must be regular")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("account digest key: permissions %o must not grant group or other access", info.Mode().Perm())
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("account digest key: read: %w", err)
	}
	return contents, nil
}

func createAccountDigestKey(path string, contents []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".model-combo-account-digest-*")
	if err != nil {
		return fmt.Errorf("account digest key: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("account digest key: set temporary file permissions: %w", err)
	}
	if _, err := temp.Write(contents); err != nil {
		temp.Close()
		return fmt.Errorf("account digest key: write: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("account digest key: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("account digest key: close: %w", err)
	}
	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fs.ErrExist
		}
		return fmt.Errorf("account digest key: publish: %w", err)
	}
	return nil
}
