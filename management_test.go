package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// createTestStore initializes a temporary usage store for testing.
func createTestStore(t testing.TB) *usageStore {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", sqliteDSN(dbPath, "WAL"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := initializeSQLiteDatabase(db); err != nil {
		db.Close()
		t.Fatalf("initializeSQLiteDatabase: %v", err)
	}
	store := &usageStore{db: db}
	return store
}

func TestHandleHealthListEmpty(t *testing.T) {
	store := createTestStore(t)
	defer store.Close()

	plugin := &modelComboPlugin{
		store:        store,
		comboRuntime: newComboRuntime(time.Now),
	}

	resp := handleHealthList(plugin)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("handleHealthList() status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result healthListResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	// Empty store should return empty ComboHealth and count 0.
	if result.Count != 0 {
		t.Errorf("Count = %d, want 0", result.Count)
	}
	if len(result.ComboHealth) != 0 {
		t.Errorf("ComboHealth size = %d, want 0", len(result.ComboHealth))
	}
}

func TestHandleHealthListNoSecrets(t *testing.T) {
	store := createTestStore(t)
	defer store.Close()

	plugin := &modelComboPlugin{
		store:        store,
		comboRuntime: newComboRuntime(time.Now),
	}

	// Insert test health data with names that could contain secrets.
	if err := store.SaveComboHealthSnapshot("combo1", "group1", "cand1", &candidateHealth{
		CoolUntil:           time.Now(),
		ConsecutiveFailures: 0,
		CircuitOpen:         false,
	}); err != nil {
		t.Fatalf("SaveComboHealthSnapshot() error = %v", err)
	}

	resp := handleHealthList(plugin)

	// Verify no sensitive strings are exposed.
	forbiddenPatterns := []string{
		"AuthID", "AuthIndex", "authId", "authIndex",
		"email", "credential", "token",
		".pem", ".key", "password",
	}
	for _, pattern := range forbiddenPatterns {
		if bytes.Contains(resp.Body, []byte(pattern)) {
			t.Errorf("response contains forbidden pattern: %s", pattern)
		}
	}

	// Verify valid structure keys are present.
	if !bytes.Contains(resp.Body, []byte("comboHealth")) {
		t.Error("response missing 'comboHealth' key")
	}
	if !bytes.Contains(resp.Body, []byte("\"count\"")) {
		t.Error("response missing 'count' key")
	}
}

func TestHandleHealthResetScoped(t *testing.T) {
	store := createTestStore(t)
	defer store.Close()

	plugin := &modelComboPlugin{
		store:        store,
		comboRuntime: newComboRuntime(time.Now),
	}

	// Insert health data for two different groups.
	if err := store.SaveComboHealthSnapshot("combo1", "group1", "cand1", &candidateHealth{
		CoolUntil:           time.Now().Add(5 * time.Second),
		ConsecutiveFailures: 1,
	}); err != nil {
		t.Fatalf("SaveComboHealthSnapshot group1 error = %v", err)
	}
	if err := store.SaveComboHealthSnapshot("combo1", "group2", "cand1", &candidateHealth{
		CoolUntil:           time.Now().Add(5 * time.Second),
		ConsecutiveFailures: 2,
	}); err != nil {
		t.Fatalf("SaveComboHealthSnapshot group2 error = %v", err)
	}

	// Reset only group1.
	reqBody, _ := json.Marshal(healthResetRequest{
		ComboAliasKey: "combo1",
		GroupKey:      "group1",
	})

	resp := handleHealthReset(plugin, reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("handleHealthReset() status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Verify group1 is cleared, group2 remains.
	snapshot, err := store.HydrateComboHealth()
	if err != nil {
		t.Fatalf("HydrateComboHealth() error = %v", err)
	}

	if group1, ok := snapshot["combo1"]["group1"]; ok && len(group1) > 0 {
		t.Errorf("group1 not cleared: %#v", group1)
	}

	if group2, ok := snapshot["combo1"]["group2"]; !ok || len(group2) == 0 {
		t.Error("group2 was incorrectly cleared")
	}
}

func TestHandleHealthManagementExactPaths(t *testing.T) {
	store := createTestStore(t)
	defer store.Close()

	plugin := &modelComboPlugin{
		store:        store,
		comboRuntime: newComboRuntime(time.Now),
	}

	tests := []struct {
		name         string
		path         string
		method       string
		wantStatus   int
		wantNotFound bool
	}{
		{
			name:         "exact list GET",
			path:         "/v0/management/plugins/model-combo/health/list",
			method:       http.MethodGet,
			wantStatus:   http.StatusOK,
			wantNotFound: false,
		},
		{
			name:         "exact reset POST",
			path:         "/v0/management/plugins/model-combo/health/reset",
			method:       http.MethodPost,
			wantStatus:   http.StatusBadRequest,
			wantNotFound: false,
		},
		{
			name:         "wrong plugin suffix (loose matching attack)",
			path:         "/v0/management/plugins/model-combo-alt/health/list",
			method:       http.MethodGet,
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:         "extra path segment",
			path:         "/v0/management/plugins/model-combo/health/list/extra",
			method:       http.MethodGet,
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:         "list wrong method",
			path:         "/v0/management/plugins/model-combo/health/list",
			method:       http.MethodPost,
			wantStatus:   http.StatusMethodNotAllowed,
			wantNotFound: false,
		},
		{
			name:         "reset wrong method",
			path:         "/v0/management/plugins/model-combo/health/reset",
			method:       http.MethodGet,
			wantStatus:   http.StatusMethodNotAllowed,
			wantNotFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := pluginapi.ManagementRequest{
				Path:   tt.path,
				Method: tt.method,
				Body:   []byte("{}"),
			}
			resp := handleHealthManagement(plugin, tt.path, req)

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}

			hasNotFound := bytes.Contains(resp.Body, []byte("not_found"))
			if tt.wantNotFound && !hasNotFound {
				t.Error("expected not_found error")
			}
			if !tt.wantNotFound && hasNotFound {
				t.Errorf("got unexpected not_found error: %s", resp.Body)
			}
		})
	}
}

func TestHandleHealthResetValidation(t *testing.T) {
	store := createTestStore(t)
	defer store.Close()

	plugin := &modelComboPlugin{
		store:        store,
		comboRuntime: newComboRuntime(time.Now),
	}

	tests := []struct {
		name       string
		body       []byte
		wantStatus int
		wantError  string
	}{
		{
			name:       "empty comboAliasKey",
			body:       []byte(`{"comboAliasKey": "", "groupKey": "g1"}`),
			wantStatus: http.StatusBadRequest,
			wantError:  "missing_keys",
		},
		{
			name:       "empty groupKey",
			body:       []byte(`{"comboAliasKey": "c1", "groupKey": ""}`),
			wantStatus: http.StatusBadRequest,
			wantError:  "missing_keys",
		},
		{
			name:       "invalid JSON",
			body:       []byte(`{invalid}`),
			wantStatus: http.StatusBadRequest,
			wantError:  "decode_failed",
		},
		{
			name:       "trailing data",
			body:       []byte(`{"comboAliasKey": "c1", "groupKey": "g1"} extra`),
			wantStatus: http.StatusBadRequest,
			wantError:  "extra_data",
		},
		{
			name:       "unknown fields",
			body:       []byte(`{"comboAliasKey": "c1", "groupKey": "g1", "unknown": true}`),
			wantStatus: http.StatusBadRequest,
			wantError:  "decode_failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := handleHealthReset(plugin, tt.body)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if !bytes.Contains(resp.Body, []byte(tt.wantError)) {
				t.Errorf("body missing expected error %q: %s", tt.wantError, resp.Body)
			}
		})
	}
}
