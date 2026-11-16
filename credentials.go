package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const maxCredentialLabelRunes = 80

type credentialSnapshot struct {
	label     string
	id        string
	ambiguous bool // an AuthIndex shared by several IDs is never resolved by index
}

type credentialSnapshotMaps struct {
	byID    map[string]string
	byIndex map[string]credentialSnapshot
}

// credentialSnapshots holds immutable safe display labels. Catalog refreshes require a host callback
// context; usage ingestion only reads the latest map and never blocks on a host lookup.
type credentialSnapshots struct {
	mu      sync.Mutex
	current atomic.Pointer[credentialSnapshotMaps]
}

func (s *credentialSnapshots) replace(entries []pluginapi.HostAuthFileEntry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := &credentialSnapshotMaps{byID: map[string]string{}, byIndex: map[string]credentialSnapshot{}}
	for _, entry := range entries {
		id, index := strings.TrimSpace(entry.ID), strings.TrimSpace(entry.AuthIndex)
		label := safeCredentialLabel(entry.Label, entry.Email)
		if id == "" {
			continue
		}
		next.byID[id] = label
		if index == "" {
			continue
		}
		if existing, found := next.byIndex[index]; found {
			if existing.id != id || existing.ambiguous {
				existing.ambiguous = true
				next.byIndex[index] = existing
			} else {
				next.byIndex[index] = credentialSnapshot{label: label, id: id}
			}
		} else {
			next.byIndex[index] = credentialSnapshot{label: label, id: id}
		}
	}
	s.current.Store(next)
}

// label resolves by exact credential ID, or by an unambiguous index when the ID is absent.
// Empty means unavailable: callers show a neutral label instead of guessing.
func (s *credentialSnapshots) label(authID, authIndex string) string {
	if s == nil {
		return ""
	}
	maps := s.current.Load()
	if maps == nil {
		return ""
	}
	if id := strings.TrimSpace(authID); id != "" {
		return maps.byID[id]
	}
	if snapshot, found := maps.byIndex[strings.TrimSpace(authIndex)]; found && !snapshot.ambiguous {
		return snapshot.label
	}
	return ""
}

// safeCredentialLabel prefers Label then Email; source, paths, tokens and metadata are never inputs.
func safeCredentialLabel(candidates ...string) string {
	for _, candidate := range candidates {
		label := strings.Join(strings.FieldsFunc(candidate, unicode.IsControl), " ")
		label = strings.TrimSpace(label)
		if runes := []rune(label); len(runes) > maxCredentialLabelRunes {
			label = strings.TrimSpace(string(runes[:maxCredentialLabelRunes]))
		}
		if label != "" && !credentialShapedLabel(label) {
			return label
		}
	}
	return ""
}

func credentialShapedLabel(value string) bool {
	if hasCredentialPrefix(value) || strings.HasPrefix(value, "eyJ") {
		return true
	}
	// shortcut: opaque 32+ char tokens are treated as secrets; upgrade if real labels trip this.
	if len(value) < 32 || strings.ContainsAny(value, " @.") {
		return false
	}
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
