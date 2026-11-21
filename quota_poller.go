package main

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"sync"
	"time"
)

type quotaEntry struct {
	credential accountCredential
	snapshot   quotaSnapshot
	used       time.Time
	flight     chan struct{}
	failures   int
}
type quotaManager struct {
	mu         sync.Mutex
	config     quotaPollingConfig
	entries    map[string]*quotaEntry
	metadata   map[string]quotaMetadata
	selections map[string]map[string]any
	running    bool
	enabled    bool
	generation uint64
	lastTick   time.Time
	semaphore  chan struct{}
	now        func() time.Time
}

func newQuotaManager(cfg quotaPollingConfig) *quotaManager {
	return &quotaManager{config: cfg, entries: make(map[string]*quotaEntry), metadata: make(map[string]quotaMetadata), selections: make(map[string]map[string]any), enabled: true, semaphore: make(chan struct{}, cfg.MaxConcurrent), now: time.Now}
}
func (m *quotaManager) Reconcile(cfg quotaPollingConfig, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = cfg
	m.enabled = enabled
	m.semaphore = make(chan struct{}, cfg.MaxConcurrent)
	if !enabled {
		m.generation++
		m.entries = make(map[string]*quotaEntry)
	}
}
func (m *quotaManager) UpdateAuthEntries(entries []pluginapi.HostAuthFileEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metadata = make(map[string]quotaMetadata, len(entries))
	for _, e := range entries {
		m.metadata[e.AuthIndex] = quotaMetadata{project: e.ProjectID, account: e.Account}
	}
}
func (m *quotaManager) RecordSelection(alias, model string, c accountCredential, score float64, state quotaState, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.selections == nil {
		m.selections = make(map[string]map[string]any)
	}
	m.selections[alias] = map[string]any{"route": alias, "model": model, "label": c.Label, "provider": c.Provider, "score": score, "score_percent": score * 100, "state": state.String(), "reason": reason}
}
func (m *quotaManager) setSnapshot(c accountCredential, snapshot quotaSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := quotaKey(c)
	e := m.entries[key]
	if e == nil {
		e = &quotaEntry{credential: c}
		m.entries[key] = e
	}
	e.snapshot = snapshot
	e.used = m.now()
}
func (m *quotaManager) Get(creds []accountCredential) map[string]quotaSnapshot {
	m.mu.Lock()
	now := m.now()
	var waits []chan struct{}
	if !m.enabled {
		m.mu.Unlock()
		return map[string]quotaSnapshot{}
	}
	for _, c := range creds {
		key := quotaKey(c)
		e := m.entries[key]
		if e == nil {
			e = &quotaEntry{credential: c}
			m.entries[key] = e
		}
		e.credential = c
		e.used = now
		cold := e.snapshot.FetchedAt.IsZero()
		expired := cold || now.Sub(e.snapshot.FetchedAt) >= time.Duration(m.config.TTLSeconds)*time.Second
		if e.flight == nil && expired && !now.Before(e.snapshot.BackoffUntil) {
			m.startFetchLocked(key, e)
		}
		if cold && e.flight != nil {
			waits = append(waits, e.flight)
		}
	}
	if len(m.entries) > 0 && !m.running {
		m.running = true
		go m.loop()
	}
	wait := time.Duration(m.config.ColdWaitMS) * time.Millisecond
	m.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for _, ch := range waits {
		select {
		case <-ch:
		case <-timer.C:
			goto done
		}
	}
done:
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]quotaSnapshot, len(creds))
	for _, c := range creds {
		if e := m.entries[quotaKey(c)]; e != nil {
			result[quotaKey(c)] = m.currentLocked(e, m.now())
		}
	}
	return result
}
func (m *quotaManager) currentLocked(e *quotaEntry, now time.Time) quotaSnapshot {
	s := e.snapshot
	if s.FetchedAt.IsZero() {
		return quotaSnapshot{Err: "poll_pending"}
	}
	if now.Sub(s.FetchedAt) > time.Duration(m.config.TTLSeconds)*time.Second {
		return quotaSnapshot{Err: "stale", FetchedAt: s.FetchedAt}
	}
	return s
}
func (m *quotaManager) startFetchLocked(key string, e *quotaEntry) {
	ch := make(chan struct{})
	e.flight = ch
	gen := m.generation
	cfg := m.config
	sem := m.semaphore
	credential := e.credential
	metadata := m.metadata[credential.AuthIndex]
	go func() {
		defer close(ch)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.FetchTimeoutSeconds)*time.Second)
		defer cancel()
		var s quotaSnapshot
		select {
		case sem <- struct{}{}:
			s = fetchQuota(ctx, credential, metadata)
			<-sem
		case <-ctx.Done():
			s = quotaSnapshot{Err: "poll_timeout"}
		}
		now := m.now()
		s.FetchedAt = now
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.generation != gen || m.entries[key] != e {
			return
		}
		e.flight = nil
		if s.State == quotaUnknown {
			e.failures++
			if s.Err == "upstream_unavailable" || s.Err == "management_request_failed" || s.Err == "poll_timeout" {
				delay := time.Duration(cfg.RefreshSeconds) * time.Second
				for i := 1; i < e.failures && delay < time.Duration(cfg.MaxStaleSeconds)*time.Second; i++ {
					delay *= 2
				}
				if max := time.Duration(cfg.MaxStaleSeconds) * time.Second; delay > max {
					delay = max
				}
				s.BackoffUntil = now.Add(delay)
			}
		} else {
			e.failures = 0
		}
		e.snapshot = s
	}()
}
func (m *quotaManager) loop() {
	for {
		m.mu.Lock()
		interval := time.Duration(m.config.RefreshSeconds) * time.Second
		m.mu.Unlock()
		timer := time.NewTimer(interval)
		<-timer.C
		m.mu.Lock()
		now := m.now()
		m.lastTick = now
		for key, e := range m.entries {
			if now.Sub(e.used) > time.Duration(m.config.IdleSeconds)*time.Second {
				delete(m.entries, key)
				continue
			}
			if e.flight == nil && now.Sub(e.snapshot.FetchedAt) >= time.Duration(m.config.RefreshSeconds)*time.Second && !now.Before(e.snapshot.BackoffUntil) {
				m.startFetchLocked(key, e)
			}
		}
		if len(m.entries) == 0 || !m.enabled {
			m.running = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}
func quotaDiagnostics(p *modelRouterPlugin) any {
	if p == nil || p.quota == nil {
		return map[string]any{"running": false, "active_keys": 0, "last_tick": time.Time{}, "credentials": []any{}, "last_selection": []any{}}
	}
	m := p.quota
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	entries := make([]map[string]any, 0, len(m.entries))
	for _, e := range m.entries {
		s := m.currentLocked(e, now)
		score, state := headroom(s, "")
		source := "observed"
		if state == quotaUnknown {
			score = m.config.DefaultUnknownQuotaPercent / 100
			source = "configured-default"
		}
		age := 0.0
		if !s.FetchedAt.IsZero() {
			age = now.Sub(s.FetchedAt).Seconds()
		}
		entries = append(entries, map[string]any{"provider": e.credential.Provider, "auth_index": e.credential.AuthIndex, "label": e.credential.Label, "state": state.String(), "windows": s.Windows, "source": s.Source, "fetched_at": s.FetchedAt, "age_seconds": age, "reason": s.Err, "effective_percent": score * 100, "score_source": source, "backoff_until": s.BackoffUntil})
	}
	selections := make([]map[string]any, 0, len(m.selections))
	for _, selection := range m.selections {
		selections = append(selections, selection)
	}
	return map[string]any{"running": m.running, "active_keys": len(m.entries), "last_tick": m.lastTick, "credentials": entries, "last_selection": selections}
}
