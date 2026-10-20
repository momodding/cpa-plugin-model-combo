package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSchemaV1toV3Migration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open bare DB
	db, err := sql.Open("sqlite3", sqliteDSN(dbPath, "WAL"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	// Create v1 minimal schema (simulating old database)
	if _, err := db.Exec(`CREATE TABLE store_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		storage_schema_version INTEGER NOT NULL,
		next_sequence INTEGER NOT NULL DEFAULT 0,
		last_prune_day TEXT,
		prices_json BLOB,
		preferences_json BLOB
	)`); err != nil {
		t.Fatalf("create v1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO store_state(id, storage_schema_version) VALUES (1, 1)`); err != nil {
		t.Fatalf("insert v1: %v", err)
	}

	// Verify v1 before migration
	var version int
	if err := db.QueryRow("SELECT storage_schema_version FROM store_state WHERE id = 1").Scan(&version); err != nil {
		t.Fatalf("query v1: %v", err)
	}
	if version != 1 {
		t.Errorf("want v1, got %d", version)
	}

	// Run full initialization (detects v1, migrates)
	if err := initializeSQLiteDatabase(db); err != nil {
		t.Fatalf("initializeSQLiteDatabase: %v", err)
	}

	// Verify current schema after migration
	if err := db.QueryRow("SELECT storage_schema_version FROM store_state WHERE id = 1").Scan(&version); err != nil {
		t.Fatalf("query v2: %v", err)
	}
	if version != usageStorageSchemaVersion {
		t.Errorf("want current schema v%d, got %d", usageStorageSchemaVersion, version)
	}

	// Verify combo_health_v2 table exists and is writable
	if _, err := db.Exec(`INSERT INTO combo_health_v2(combo_alias_key, group_key, candidate_key, snapshot_json, updated_at_ns)
		VALUES (?, ?, ?, ?, ?)`, "test", "g1", "c1", []byte("{}"), time.Now().UnixNano()); err != nil {
		t.Fatalf("insert health post-migration: %v", err)
	}
}

func TestNewDatabaseInitializationV3(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "new.db")

	// Open new database (no store_state exists yet)
	db, err := sql.Open("sqlite3", sqliteDSN(dbPath, "WAL"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	// Initialize new database with current schema
	if err := initializeSQLiteDatabase(db); err != nil {
		t.Fatalf("initializeSQLiteDatabase new: %v", err)
	}

	// Verify current schema
	var version int
	if err := db.QueryRow("SELECT storage_schema_version FROM store_state WHERE id = 1").Scan(&version); err != nil {
		t.Fatalf("query: %v", err)
	}
	if version != usageStorageSchemaVersion {
		t.Errorf("want current schema v%d, got %d", usageStorageSchemaVersion, version)
	}

	// Verify all v2 tables exist
	tables := []string{"store_state", "requests", "minute_aggregates", "combo_health_v2"}
	for _, table := range tables {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s not found", table)
		}
	}
}

func TestRejectFutureSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "future.db")

	// Open database and create future schema
	db, err := sql.Open("sqlite3", sqliteDSN(dbPath, "WAL"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE store_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		storage_schema_version INTEGER NOT NULL,
		next_sequence INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO store_state(id, storage_schema_version) VALUES (1, 999)`); err != nil {
		t.Fatalf("insert future version: %v", err)
	}

	// Should reject future schema
	if err := initializeSQLiteDatabase(db); err == nil {
		t.Error("expected error for future schema version")
	} else if !strings.Contains(err.Error(), "newer than supported") {
		t.Errorf("wrong error: %v", err)
	}
}
func TestFreshDatabaseInitialization(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fresh.db")

	// Bare open on non-existent file—no tables yet
	db, err := sql.Open("sqlite3", sqliteDSN(dbPath, "WAL"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	// Query store_state returns "no such table" error, not sql.ErrNoRows
	// initializeSQLiteDatabase must detect this and create current schema
	if err := initializeSQLiteDatabase(db); err != nil {
		t.Fatalf("initializeSQLiteDatabase fresh: %v", err)
	}

	// Verify current schema created
	var version int
	if err := db.QueryRow("SELECT storage_schema_version FROM store_state WHERE id = 1").Scan(&version); err != nil {
		t.Fatalf("query: %v", err)
	}
	if version != usageStorageSchemaVersion {
		t.Errorf("want current schema v%d, got %d", usageStorageSchemaVersion, version)
	}

	// Verify combo_health_v2 exists and works
	if _, err := db.Exec(`INSERT INTO combo_health_v2(combo_alias_key, group_key, candidate_key, snapshot_json, updated_at_ns)
		VALUES (?, ?, ?, ?, ?)`, "fresh", "g1", "c1", []byte("{}"), time.Now().UnixNano()); err != nil {
		t.Fatalf("insert health: %v", err)
	}
}

func TestSaveLoadComboHealthSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	// Create health
	health := &candidateHealth{
		CoolUntil:           time.Now().Add(5 * time.Second),
		ConsecutiveFailures: 3,
		CircuitOpen:         true,
		CircuitOpenUntil:    time.Now().Add(10 * time.Second),
		CircuitReopenCount:  2,
		HalfOpenOccupied:    false,
	}

	// Save
	if err := store.SaveComboHealthSnapshot("combo1", "g1", "c1", health); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load
	loaded, err := store.LoadComboHealthSnapshot("combo1", "g1", "c1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded == nil {
		t.Fatal("loaded is nil")
	}
	if loaded.ConsecutiveFailures != 3 {
		t.Errorf("want 3 failures, got %d", loaded.ConsecutiveFailures)
	}
	if !loaded.CircuitOpen {
		t.Error("want circuit open")
	}
	if loaded.CircuitReopenCount != 2 {
		t.Errorf("want 2 reopen, got %d", loaded.CircuitReopenCount)
	}
}

func TestLoadMissingComboHealthSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	loaded, err := store.LoadComboHealthSnapshot("combo", "g", "missing")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded != nil {
		t.Error("want nil for missing")
	}
}

func TestResetComboHealth(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	health := &candidateHealth{ConsecutiveFailures: 1}

	// Save two
	if err := store.SaveComboHealthSnapshot("combo1", "g1", "c1", health); err != nil {
		t.Fatalf("save c1: %v", err)
	}
	if err := store.SaveComboHealthSnapshot("combo1", "g1", "c2", health); err != nil {
		t.Fatalf("save c2: %v", err)
	}

	// Reset group
	if err := store.ResetComboHealth("combo1", "g1"); err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Verify deleted
	l1, _ := store.LoadComboHealthSnapshot("combo1", "g1", "c1")
	l2, _ := store.LoadComboHealthSnapshot("combo1", "g1", "c2")
	if l1 != nil || l2 != nil {
		t.Error("want both deleted")
	}
}

func TestCandidateHealthJSON(t *testing.T) {
	health := &candidateHealth{
		CoolUntil:           time.Now().Add(10 * time.Second),
		ConsecutiveFailures: 3,
		CircuitOpen:         true,
		CircuitOpenUntil:    time.Now().Add(20 * time.Second),
		CircuitReopenCount:  2,
		HalfOpenOccupied:    true,
	}

	// Marshal/unmarshal
	data, err := json.Marshal(health)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored candidateHealth
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify fields
	if restored.ConsecutiveFailures != 3 {
		t.Error("failures mismatch")
	}
	if !restored.CircuitOpen {
		t.Error("circuitOpen mismatch")
	}
	if restored.CircuitReopenCount != 2 {
		t.Error("reopenCount mismatch")
	}
	if !restored.HalfOpenOccupied {
		t.Error("halfOpenOccupied mismatch")
	}
}

func TestSaveHealthWithEmptyKeys(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	health := &candidateHealth{ConsecutiveFailures: 1}

	tests := []struct {
		name             string
		alias, grp, cand string
		wantErr          bool
	}{
		{"empty alias", "", "g1", "c1", true},
		{"empty group", "combo", "", "c1", true},
		{"empty candidate", "combo", "g1", "", true},
		{"valid", "combo", "g1", "c1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.SaveComboHealthSnapshot(tt.alias, tt.grp, tt.cand, health)
			if (err != nil) != tt.wantErr {
				t.Errorf("got error %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSaveHealthNilSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	err = store.SaveComboHealthSnapshot("combo", "g1", "c1", nil)
	if err == nil {
		t.Error("want error for nil snapshot")
	}
}

func TestClosedStoreSaveHealth(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.Close()

	health := &candidateHealth{ConsecutiveFailures: 1}
	err = store.SaveComboHealthSnapshot("combo", "g1", "c1", health)
	if err == nil {
		t.Error("want error for closed store")
	}
}

func TestHydrateComboHealth(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	// Save multiple health snapshots.
	health1 := &candidateHealth{
		ConsecutiveFailures: 2,
		CircuitOpen:         true,
		CircuitReopenCount:  1,
	}
	health2 := &candidateHealth{
		ConsecutiveFailures: 1,
		CircuitOpen:         false,
	}

	if err := store.SaveComboHealthSnapshot("combo1", "g1", "c1", health1); err != nil {
		t.Fatalf("save health1: %v", err)
	}
	if err := store.SaveComboHealthSnapshot("combo1", "g2", "c2", health2); err != nil {
		t.Fatalf("save health2: %v", err)
	}

	// Hydrate all.
	hydrated, err := store.HydrateComboHealth()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	if len(hydrated) != 1 {
		t.Errorf("want 1 combo, got %d", len(hydrated))
	}
	if len(hydrated["combo1"]) != 2 {
		t.Errorf("want 2 groups, got %d", len(hydrated["combo1"]))
	}
	if hydrated["combo1"]["g1"]["c1"].ConsecutiveFailures != 2 {
		t.Error("health1 mismatch")
	}
	if hydrated["combo1"]["g2"]["c2"].ConsecutiveFailures != 1 {
		t.Error("health2 mismatch")
	}
}

func TestPersistComboHealth(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	snapshot := map[string]map[string]map[string]*candidateHealth{
		"combo1": {
			"g1": {
				"c1": {ConsecutiveFailures: 3, CircuitOpen: true},
			},
		},
	}

	// Persist snapshot.
	if err := store.PersistComboHealth(snapshot); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// Verify persisted data (fully healthy zero-state is pruned).
	hydrated, err := store.HydrateComboHealth()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	if len(hydrated["combo1"]["g1"]) != 1 {
		t.Errorf("want 1 candidate (c1 only), got %d", len(hydrated["combo1"]["g1"]))
	}
	if hydrated["combo1"]["g1"]["c1"].ConsecutiveFailures != 3 {
		t.Error("c1 mismatch")
	}
}

func TestPruneComboHealth(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	// Save health.
	health := &candidateHealth{ConsecutiveFailures: 1}
	if err := store.SaveComboHealthSnapshot("combo", "g1", "c1", health); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Prune with threshold 0 (should remove everything).
	if err := store.PruneComboHealth(0); err != nil {
		t.Fatalf("prune: %v", err)
	}

	// Verify empty.
	hydrated, err := store.HydrateComboHealth()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(hydrated) > 0 {
		t.Errorf("want 0 entries after prune, got %d", len(hydrated))
	}
}

func TestCandidateHealthJSONZeroNanosecondsMapping(t *testing.T) {
	// Verify that zero nanoseconds marshal/unmarshal as time.Time{}, not Unix epoch.
	health := &candidateHealth{
		CoolUntil:           time.Time{},
		CircuitOpenUntil:    time.Time{},
		ConsecutiveFailures: 0,
		CircuitOpen:         false,
	}

	data, err := json.Marshal(health)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored candidateHealth
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify zero times remain zero, not Unix epoch.
	if !restored.CoolUntil.IsZero() {
		t.Errorf("want zero coolUntil, got %v", restored.CoolUntil)
	}
	if !restored.CircuitOpenUntil.IsZero() {
		t.Errorf("want zero circuitOpenUntil, got %v", restored.CircuitOpenUntil)
	}
}

// TestTwoGroupHealthRegression verifies that scoped PersistComboHealth
// does not affect another group's health state.
func TestTwoGroupHealthRegression(t *testing.T) {
	dir := t.TempDir()
	store, err := openUsageStore(filepath.Join(dir, "test.db"), 30)
	if err != nil {
		t.Fatalf("openUsageStore: %v", err)
	}
	defer store.Close()

	// Step 1: Persist nonhealthy candidates in two distinct groups
	healthA := &candidateHealth{
		ConsecutiveFailures: 3,
		CircuitOpen:         true,
		CircuitReopenCount:  2,
	}
	healthB := &candidateHealth{
		ConsecutiveFailures: 5,
		CircuitOpen:         true,
		CircuitReopenCount:  1,
	}

	err = store.SaveComboHealthSnapshot("combo1", "groupA", "candidateA", healthA)
	if err != nil {
		t.Fatalf("SaveComboHealthSnapshot A: %v", err)
	}
	err = store.SaveComboHealthSnapshot("combo1", "groupB", "candidateB", healthB)
	if err != nil {
		t.Fatalf("SaveComboHealthSnapshot B: %v", err)
	}

	// Step 2: PersistComboHealth with only groupA (simulates mutation callback for groupA only)
	snapshot := map[string]map[string]map[string]*candidateHealth{
		"combo1": {
			"groupA": {
				"candidateA": healthA,
			},
		},
	}
	err = store.PersistComboHealth(snapshot)
	if err != nil {
		t.Fatalf("PersistComboHealth: %v", err)
	}

	// Step 3: Hydrate and verify groupB is still present
	hydrated, err := store.HydrateComboHealth()
	if err != nil {
		t.Fatalf("HydrateComboHealth: %v", err)
	}

	// Check "combo1:groupB:candidateB" remains unchanged
	found := false
	for comboKey, groupMap := range hydrated {
		if comboKey == "combo1" {
			for groupKey, candMap := range groupMap {
				if groupKey == "groupB" {
					if _, ok := candMap["candidateB"]; ok {
						found = true
						if candMap["candidateB"].ConsecutiveFailures != 5 {
							t.Errorf("want candidateB failures=5, got %d", candMap["candidateB"].ConsecutiveFailures)
						}
						if candMap["candidateB"].CircuitReopenCount != 1 {
							t.Errorf("want candidateB CircuitReopenCount=1, got %d", candMap["candidateB"].CircuitReopenCount)
						}
					}
				}
			}
		}
	}
	if !found {
		t.Error("candidateB in groupB not found after PersistComboHealth(groupA only)")
	}
}

// TestRestoreHealthFiltersUnknownCandidates verifies RestoreHealth rejects
// candidates not in groupRuntime's candidate plan.
func TestRestoreHealthFiltersUnknownCandidates(t *testing.T) {
	// Create runtime with one group containing known candidates
	plans := []comboPlan{
		{
			aliasKey: "combo1",
			groups: []groupPlan{
				{
					groupKey: "group1",
					candidates: []candidatePlan{
						{candidateKey: "known1"},
						{candidateKey: "known2"},
					},
				},
			},
		},
	}
	rt := newComboRuntime(time.Now)
	rt.Sync(plans)

	// Prepare snapshot with known + unknown candidates
	// Keys match runtimeKey format: comboKey + "\x00" + groupKey
	snapshot := map[string]map[string]*candidateHealth{
		"combo1\x00group1": {
			"known1":   {ConsecutiveFailures: 3, CircuitOpen: true},
			"known2":   {ConsecutiveFailures: 0},
			"unknown3": {ConsecutiveFailures: 5, CircuitOpen: true}, // Not in plan
		},
	}

	// Restore: should accept only known candidates
	rt.RestoreHealth(snapshot)

	// Verify known candidates present
	gr := rt.groups["combo1\x00group1"]
	if gr == nil {
		t.Fatal("group not found")
	}

	if h, ok := gr.health["known1"]; !ok || h.ConsecutiveFailures != 3 {
		t.Error("known1 not restored correctly")
	}
	if _, ok := gr.health["known2"]; !ok {
		t.Error("known2 not restored")
	}

	// unknown3 should be rejected by RestoreHealth (not in candidateKeys)
	if _, found := gr.health["unknown3"]; found {
		t.Error("unknown3 was restored but should have been filtered out")
	}
}

// TestRestoreHealthCallerPointerMutation verifies RestoreHealth clones
// incoming health pointers; mutations to input fields do not affect restored runtime.
func TestRestoreHealthCallerPointerMutation(t *testing.T) {
	plans := []comboPlan{
		{
			aliasKey: "combo1",
			groups: []groupPlan{
				{
					groupKey: "group1",
					candidates: []candidatePlan{
						{candidateKey: "cand1"},
					},
				},
			},
		},
	}
	rt := newComboRuntime(time.Now)
	rt.Sync(plans)

	// Prepare snapshot: caller-owned pointer
	health := &candidateHealth{
		ConsecutiveFailures: 2,
		CircuitOpen:         true,
		CircuitReopenCount:  1,
	}
	snapshot := map[string]map[string]*candidateHealth{
		"combo1\x00group1": {
			"cand1": health,
		},
	}

	// Restore snapshot
	rt.RestoreHealth(snapshot)

	// Mutate input pointer after restore
	health.ConsecutiveFailures = 99
	health.CircuitOpen = false
	health.CircuitReopenCount = 42

	// Verify runtime retained original values (immune to mutation)
	gr := rt.groups["combo1\x00group1"]
	if gr == nil {
		t.Fatal("group not found")
	}
	if h, ok := gr.health["cand1"]; !ok {
		t.Error("cand1 not restored")
	} else {
		if h.ConsecutiveFailures != 2 {
			t.Errorf("want ConsecutiveFailures=2, got %d (mutation leaked)", h.ConsecutiveFailures)
		}
		if h.CircuitOpen != true {
			t.Errorf("want CircuitOpen=true, got %v (mutation leaked)", h.CircuitOpen)
		}
		if h.CircuitReopenCount != 1 {
			t.Errorf("want CircuitReopenCount=1, got %d (mutation leaked)", h.CircuitReopenCount)
		}
	}
}

func TestHealthPersistenceResetDeletesAfterInFlightWrite(t *testing.T) {
	plugin := testComboPlugin(testModelRoute("smart", routeStrategyPriority, 0, "a"))
	plan := plugin.plans[0]
	group := plan.groups[0]
	candidate := group.candidates[0]
	store, err := openUsageStore(filepath.Join(t.TempDir(), "test.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := startHealthPersistence(store, 1)
	defer p.Close()
	p.SetPlans(plugin.plans)
	started := make(chan struct{})
	release := make(chan struct{})
	p.beforePersist = func() {
		close(started)
		<-release
	}
	runtime := plugin.comboRuntime
	p.Enqueue(map[string]map[string]map[string]*candidateHealth{
		plan.aliasKey: {group.groupKey: {candidate.candidateKey: {ConsecutiveFailures: 1}}},
	})
	<-started
	resetDone := make(chan error, 1)
	go func() {
		resetDone <- runtime.ResetComboGroupHealthAndPersist(plan.aliasKey, group.groupKey, func() error {
			if err := p.Flush(); err != nil {
				return err
			}
			p.Cancel(plan.aliasKey, group.groupKey)
			return store.ResetComboHealth(plan.aliasKey, group.groupKey)
		})
	}()
	select {
	case <-resetDone:
		t.Fatal("reset completed before in-flight write released")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadComboHealthSnapshot(plan.aliasKey, group.groupKey, candidate.candidateKey)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("health row survived reset: %#v", got)
	}
}

func TestHealthPersistenceFlushRetainsFailedSnapshot(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	store := &usageStore{db: db}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	persistence := startHealthPersistence(store, 1)
	t.Cleanup(func() { _ = persistence.Close() })
	snapshot := map[string]map[string]map[string]*candidateHealth{
		"combo": {
			"group": {"candidate": {}},
		},
	}
	if !persistence.Enqueue(snapshot) {
		t.Fatal("enqueue failed")
	}
	if err := persistence.Flush(); err == nil {
		t.Fatal("Flush succeeded after failed write")
	}

	persistence.mu.Lock()
	_, ok := persistence.pending[runtimeGroupKey("combo", "group")]
	persistence.mu.Unlock()
	if !ok {
		t.Fatal("failed snapshot was lost")
	}
}
