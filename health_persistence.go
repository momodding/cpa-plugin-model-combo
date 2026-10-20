package main

import (
	"errors"
	"strings"
	"sync"
)

const defaultHealthPersistenceCapacity = 128

// healthPersistence serializes asynchronous health writes. It keeps only the
// newest snapshot per combo/group. When full, it drops a new distinct group;
// existing groups still coalesce. Health mutations never wait for SQLite.
type healthPersistence struct {
	store         *usageStore
	capacity      int
	plans         []comboPlan
	beforePersist func()
	mu            sync.Mutex
	pending       map[string]map[string]map[string]*candidateHealth
	closed        bool
	lastErr       error
	wake          chan struct{}
	flush         chan chan error
	stop          chan chan error
	done          chan struct{}
}

func startHealthPersistence(store *usageStore, capacity int) *healthPersistence {
	if capacity <= 0 {
		capacity = defaultHealthPersistenceCapacity
	}
	p := &healthPersistence{
		store: store, pending: make(map[string]map[string]map[string]*candidateHealth), capacity: capacity,
		wake: make(chan struct{}, 1), flush: make(chan chan error), stop: make(chan chan error), done: make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *healthPersistence) SetPlans(plans []comboPlan) {
	p.mu.Lock()
	p.plans = append(p.plans[:0], plans...)
	p.mu.Unlock()
}

func (p *healthPersistence) Enqueue(snapshot map[string]map[string]map[string]*candidateHealth) bool {
	if p == nil || len(snapshot) == 0 {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	accepted := true
	for comboKey, groups := range snapshot {
		for groupKey, candidates := range groups {
			key := runtimeGroupKey(comboKey, groupKey)
			if _, exists := p.pending[key]; !exists && len(p.pending) == p.capacity {
				accepted = false // Explicit overflow policy: reject newest distinct group.
				continue
			}
			copyCandidates := make(map[string]*candidateHealth, len(candidates))
			for candidateKey, health := range candidates {
				if health == nil {
					continue
				}
				healthCopy := *health
				copyCandidates[candidateKey] = &healthCopy
			}
			p.pending[key] = map[string]map[string]*candidateHealth{groupKey: copyCandidates}
		}
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return accepted
}

func (p *healthPersistence) takePending() map[string]map[string]map[string]*candidateHealth {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pending) == 0 {
		return nil
	}
	result := make(map[string]map[string]map[string]*candidateHealth, len(p.pending))
	for key, groups := range p.pending {
		comboKey, _, _ := strings.Cut(key, "\x00")
		if result[comboKey] == nil {
			result[comboKey] = make(map[string]map[string]*candidateHealth)
		}
		for groupKey, candidates := range groups {
			result[comboKey][groupKey] = candidates
		}
		delete(p.pending, key)
	}
	return result
}

func (p *healthPersistence) requeuePending(snapshot map[string]map[string]map[string]*candidateHealth) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for comboKey, groups := range snapshot {
		for groupKey, candidates := range groups {
			key := runtimeGroupKey(comboKey, groupKey)
			if _, ok := p.pending[key]; !ok {
				p.pending[key] = map[string]map[string]*candidateHealth{groupKey: candidates}
			}
		}
	}
}

func (p *healthPersistence) run() {
	defer close(p.done)
	for {
		select {
		case <-p.wake:
			_ = p.persistPending()
		case response := <-p.flush:
			response <- p.persistPending()
		case response := <-p.stop:
			response <- p.persistPending()
			return
		}
	}
}

func (p *healthPersistence) persistPending() error {
	for {
		snapshot := p.takePending()
		if len(snapshot) == 0 {
			return nil
		}
		p.mu.Lock()
		plans := append([]comboPlan(nil), p.plans...)
		beforePersist := p.beforePersist
		p.mu.Unlock()
		if beforePersist != nil {
			beforePersist()
		}
		err := p.store.PersistComboHealthForPlans(snapshot, plans)
		if err != nil {
			p.requeuePending(snapshot)
			p.mu.Lock()
			p.lastErr = err
			p.mu.Unlock()
			return err
		}
	}
}
func (p *healthPersistence) Flush() error {
	if p == nil {
		return nil
	}
	response := make(chan error, 1)
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return errors.New("health persistence is closed")
	}
	select {
	case p.flush <- response:
		return <-response
	case <-p.done:
		return errors.New("health persistence is closed")
	}
}

func (p *healthPersistence) Cancel(comboAliasKey, groupKey string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	key := runtimeGroupKey(comboAliasKey, groupKey)
	delete(p.pending, key)
	p.mu.Unlock()
}

func (p *healthPersistence) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	response := make(chan error, 1)
	p.stop <- response
	err := <-response
	<-p.done
	return err
}
