package main

import (
	"os"
	"strings"
	"testing"
)

// TestDeployConfigTemplateStructure validates deploy/podman/config.yaml.template
// matches Combo v2 parser schema expectations (evidence-based, no mocking).
func TestDeployConfigTemplateStructure(t *testing.T) {
	templatePath := "deploy/podman/config.yaml.template"
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	// Verify key sections exist in template
	requiredSections := []string{
		`host: "127.0.0.1"`,              // loopback default
		`port: 8317`,                     // standard CPA port
		`api-keys:`,                      // auth required
		`- "@API_KEY@"`,                  // placeholder for substitution
		`remote-management:`,             // dashboard management
		`secret-key: "@MANAGEMENT_KEY@"`, // mgmt placeholder
		`plugins:`,                       // plugin config section
		`enabled: true`,                  // must enable plugin
		`dir: "/CLIProxyAPI/plugins"`,    // container plugin mount
		`model-combo:`,                   // combo config block
		`data_path: "/CLIProxyAPI/data/`, // SQLite path (container)
		`retention_days: 365`,            // retention config
		`resilience:`,                    // resilience section
		`routes:`,                        // routes required for Combo v2
	}
	for _, section := range requiredSections {
		if !strings.Contains(string(raw), section) {
			t.Errorf("template missing section: %q", section)
		}
	}
}

// TestDeployConfigTemplateLoopbackDefault ensures loopback binding is enforced.
func TestDeployConfigTemplateLoopbackDefault(t *testing.T) {
	templatePath := "deploy/podman/config.yaml.template"
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	// Verify host is localhost, not 0.0.0.0
	if !strings.Contains(string(raw), `host: "127.0.0.1"`) {
		t.Error("host must be 127.0.0.1 (loopback), not 0.0.0.0 or other address")
	}
	if strings.Contains(string(raw), `host: "0.0.0.0"`) {
		t.Error("template must not expose 0.0.0.0 by default; use reverse proxy in production")
	}
}

// TestDeployComposeStructure validates deploy/podman/compose.yaml essential bindings.
func TestDeployComposeStructure(t *testing.T) {
	composePath := "deploy/podman/compose.yaml"
	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	composeStr := string(raw)

	// Verify required volume bindings
	requiredBindings := []string{
		"/CLIProxyAPI/config.yaml", // config bind mount (ro)
		"/CLIProxyAPI/plugins",     // plugins bind mount (ro)
		"/root/.cli-proxy-api",     // auth volume
		"/CLIProxyAPI/data",        // data/SQLite volume
		"/CLIProxyAPI/logs",        // logs volume
		"/CLIProxyAPI/static",      // static assets volume
	}
	for _, binding := range requiredBindings {
		if !strings.Contains(composeStr, binding) {
			t.Errorf("compose missing volume binding: %q", binding)
		}
	}

	// Verify user/group set (non-root)
	if !strings.Contains(composeStr, `user: `) {
		t.Error("compose must set non-root user")
	}

	// Verify networking uses environment substitution
	if !strings.Contains(composeStr, "CPA_BIND_HOST") {
		t.Error("compose must allow CPA_BIND_HOST override")
	}
	if !strings.Contains(composeStr, "CPA_PORT") {
		t.Error("compose must allow CPA_PORT override")
	}
}

// TestDeployEnvExampleCompleteness ensures .env.example documents all compose vars.
func TestDeployEnvExampleCompleteness(t *testing.T) {
	envPath := "deploy/podman/.env.example"
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	envStr := string(raw)

	// Verify critical environment variables documented
	requiredVars := map[string]string{
		"CPA_IMAGE":       "container image",
		"CPA_BIND_HOST":   "network binding",
		"CPA_PORT":        "port override",
		"CPA_CONFIG_FILE": "config path",
		"CPA_PLUGIN_DIR":  "plugin directory",
	}
	for envVar := range requiredVars {
		if !strings.Contains(envStr, envVar) {
			t.Errorf(".env.example missing %s", envVar)
		}
	}
}

// TestDeployGitignoreSecretsIgnored verifies .gitignore patterns protect secrets.
func TestDeployGitignoreSecretsIgnored(t *testing.T) {
	gitignorePath := "deploy/podman/.gitignore"
	raw, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	gitignoreStr := string(raw)

	// Verify patterns
	requiredPatterns := []string{
		".env",        // .env itself
		".env.local",  // .env.local overrides
		"config.yaml", // rendered config (real values)
	}
	for _, pattern := range requiredPatterns {
		if !strings.Contains(gitignoreStr, pattern) {
			t.Errorf(".gitignore missing pattern: %q", pattern)
		}
	}

	// Verify .env.example is NOT ignored (should be tracked)
	if strings.Contains(gitignoreStr, ".env.example") {
		t.Error(".env.example should be tracked in git (contains only placeholders)")
	}
}

// TestDeployConfigPathContainer verifies container paths in template.
func TestDeployConfigPathContainer(t *testing.T) {
	templatePath := "deploy/podman/config.yaml.template"
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	// Verify container paths are used (not host paths)
	if !strings.Contains(string(raw), "/CLIProxyAPI/data/") {
		t.Error("template should use container path /CLIProxyAPI/data/ for data_path")
	}
	if !strings.Contains(string(raw), "/CLIProxyAPI/plugins") {
		t.Error("template should use container path /CLIProxyAPI/plugins for plugin dir")
	}
}

// TestDeployComposeMountReadOnly ensures config/plugins are read-only, data volumes writable.
func TestDeployComposeMountReadOnly(t *testing.T) {
	composePath := "deploy/podman/compose.yaml"
	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	composeStr := string(raw)

	// Config and plugins should be read-only (bind mounts)
	lines := strings.Split(composeStr, "\n")
	var configRO, pluginsRO bool
	for i, line := range lines {
		if strings.Contains(line, "config.yaml") && i > 0 {
			// Check previous/next 5 lines for read_only flag
			section := strings.Join(lines[max(0, i-2):min(len(lines), i+5)], "\n")
			if strings.Contains(section, "read_only: true") {
				configRO = true
			}
		}
		if strings.Contains(line, `source: ${CPA_PLUGIN_DIR`) && i > 0 {
			// Check for plugins volume (not the dir path)
			section := strings.Join(lines[max(0, i-2):min(len(lines), i+5)], "\n")
			if strings.Contains(section, "read_only: true") {
				pluginsRO = true
			}
		}
	}
	if !configRO {
		t.Error("config.yaml mount should be read-only")
	}
	if !pluginsRO {
		t.Error("plugins mount should be read-only")
	}

	// Verify persistent volumes (not read-only)
	if !strings.Contains(composeStr, "cpa_data") && !strings.Contains(composeStr, "CLIProxyAPI/data") {
		t.Error("compose must have persistent data volume")
	}
	if !strings.Contains(composeStr, "cpa_auth") && !strings.Contains(composeStr, "/root/.cli-proxy-api") {
		t.Error("compose must have persistent auth volume")
	}
}

// TestDeployPlaceholdersDocumented verifies template uses placeholders for secrets.
func TestDeployPlaceholdersDocumented(t *testing.T) {
	templatePath := "deploy/podman/config.yaml.template"
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	// Verify template uses @PLACEHOLDER@ format for secrets
	requiredPlaceholders := []string{
		"@API_KEY@",
		"@MANAGEMENT_KEY@",
	}
	for _, ph := range requiredPlaceholders {
		if !strings.Contains(string(raw), ph) {
			t.Errorf("template missing placeholder: %q", ph)
		}
	}

	// Verify no hardcoded real keys in template
	if strings.Contains(string(raw), "api-key: sk-") || strings.Contains(string(raw), "secret-key: sk-") {
		t.Error("template contains hardcoded API keys; use placeholders instead")
	}
}

// TestDeployComposeEnvVarSubstitution verifies compose uses environment overrides.
func TestDeployComposeEnvVarSubstitution(t *testing.T) {
	composePath := "deploy/podman/compose.yaml"
	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	composeStr := string(raw)

	// Verify key env var references with fallback defaults
	envVarTests := map[string]string{
		"CPA_BIND_HOST": "127.0.0.1", // binding with default
		"CPA_PORT":      "8317",      // port with default
		"CPA_UID":       "1000",      // user ID
		"CPA_GID":       "3000",      // group ID
	}
	for envVar, expectedDefault := range envVarTests {
		// Check that compose references ${VAR:-default} format
		if expectedDefault != "" {
			defaultRef := "${" + envVar + ":-" + expectedDefault + "}"
			if !strings.Contains(composeStr, defaultRef) {
				t.Errorf("compose missing env var default: %s (expected pattern %s)", envVar, defaultRef)
			}
		}
	}
}

// Helper functions
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
