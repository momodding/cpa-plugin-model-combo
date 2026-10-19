package main

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestAssembleModelComboDashboardEmbedsEveryAssetOnce(t *testing.T) {
	page, err := assembleModelComboDashboard(dashboardSource)
	if err != nil {
		t.Fatalf("assemble dashboard: %v", err)
	}
	html := string(page)
	for _, marker := range []string{
		"<title>Model Combo</title>",
		"id=\"configuration-panel\"",
		"id=\"usage-panel\"",
		"id=\"empty-template\"",
		"id=\"route-template\"",
		"id=\"target-template\"",
		"id=\"pricing-dialog\"",
		"id=\"reset-dialog\"",
		"function initializeConfigurationEvents()",
		"function initializeUsageEvents()",
		"function initializeUsageTableEvents()",
		"function initializeUsageChartEvents()",
		"function initializePricingEvents()",
		"function restoreRouteFocus(descriptor)",
		"id=\"combos-json\"",
		"function configurationSnapshot()",
		"data-account-field=\"priority\"",
		"data-account-field=\"weight\"",
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("assembled dashboard is missing %q", marker)
		}
	}
	for _, tag := range []string{"<style>", "</style>", "<script>", "</script>"} {
		if count := strings.Count(html, tag); count != 1 {
			t.Errorf("dashboard contains %d instances of %q, want 1", count, tag)
		}
	}
	for _, marker := range []string{"{{include", "<script src=", "<link rel=\"stylesheet\""} {
		if strings.Contains(html, marker) {
			t.Errorf("assembled dashboard contains unresolved or external asset marker %q", marker)
		}
	}

	err = fs.WalkDir(dashboardSource, "dashboard", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(dashboardSource, path)
		if err != nil {
			return err
		}
		if count := strings.Count(html, string(content)); count != 1 {
			t.Errorf("embedded asset %q appears %d times in the dashboard, want 1", path, count)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dashboard assets: %v", err)
	}
}

func TestAssembleModelComboDashboardReportsInvalidAssets(t *testing.T) {
	tests := []struct {
		name string
		fs   fstest.MapFS
		want string
	}{
		{
			name: "missing asset",
			fs: fstest.MapFS{
				"dashboard.html": &fstest.MapFile{Data: []byte(`{{include "dashboard.css"}}`)},
			},
			want: `read dashboard asset "dashboard.css"`,
		},
		{
			name: "invalid template",
			fs: fstest.MapFS{
				"dashboard.html": &fstest.MapFile{Data: []byte(`{{if}}`)},
			},
			want: "parse dashboard shell",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := assembleModelComboDashboard(test.fs)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("assemble error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestModelComboManagementDashboardReportsAssemblyFailure(t *testing.T) {
	previous := configDashboardError
	configDashboardError = errors.New("test dashboard assembly failure")
	t.Cleanup(func() { configDashboardError = previous })

	response := handleModelComboManagement(nil, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   modelComboDashboardPath,
	})
	if response.StatusCode != http.StatusInternalServerError || !strings.Contains(string(response.Body), "dashboard_render_failed") {
		t.Fatalf("dashboard assembly failure response = status %d, body %s", response.StatusCode, response.Body)
	}
}
