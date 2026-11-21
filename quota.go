package main

import (
	"strings"
	"time"
)

type quotaState uint8

const (
	quotaUnknown quotaState = iota
	quotaPartial
	quotaKnown
	quotaUnlimited
	quotaExhausted
)

func (s quotaState) String() string {
	switch s {
	case quotaPartial:
		return "partial"
	case quotaKnown:
		return "known"
	case quotaUnlimited:
		return "unlimited"
	case quotaExhausted:
		return "exhausted"
	}
	return "unknown"
}

type quotaWindow struct {
	Name      string    `json:"name"`
	Scope     string    `json:"scope,omitempty"`
	Remaining float64   `json:"remaining"`
	ResetAt   time.Time `json:"reset_at,omitempty"`
}
type quotaSnapshot struct {
	State        quotaState
	Windows      []quotaWindow
	Source       string
	FetchedAt    time.Time
	Err          string
	BackoffUntil time.Time
}

func quotaKey(c accountCredential) string { return strings.ToLower(c.Provider) + "|" + c.AuthIndex }
func headroom(s quotaSnapshot, model string) (float64, quotaState) {
	if s.State == quotaUnknown {
		return 0, quotaUnknown
	}
	if s.State == quotaUnlimited {
		return 1, quotaUnlimited
	}
	model = strings.ToLower(model)
	score, found := 1.0, false
	for _, w := range s.Windows {
		scope := strings.ToLower(w.Scope)
		if scope != "" && !quotaScopeMatches(scope, model) {
			continue
		}
		found = true
		if w.Remaining <= 0 {
			return 0, quotaExhausted
		}
		if w.Remaining < score {
			score = w.Remaining
		}
	}
	if !found {
		return 0, quotaUnknown
	}
	if s.State == quotaPartial {
		return score, quotaPartial
	}
	return score, quotaKnown
}
func quotaScopeMatches(scope, model string) bool {
	model = strings.ToLower(model)
	switch scope {
	case "opus":
		return strings.Contains(model, "opus")
	case "sonnet":
		return strings.Contains(model, "sonnet")
	case "gemini", "claude", "gpt":
		return strings.HasPrefix(model, scope+"-") || strings.Contains(model, "/"+scope+"-")
	}
	return strings.Contains(model, scope)
}
