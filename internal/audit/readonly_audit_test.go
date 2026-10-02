package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wa-campus-analyzer/internal/omniroute"
)

// TestAudit_ZeroOutboundGOWACalls verifies that analyzer codebase contains no calls to GOWA outbound APIs.
func TestAudit_ZeroOutboundGOWACalls(t *testing.T) {
	rootPath := "../.."

	prohibitedPatterns := []string{
		"/send/message",
		"/send/image",
		"/send/document",
		"/send/audio",
		"/send/video",
		"/send/sticker",
		"/message/mark-read",
		"/message/read",
		"/message/react",
		"/user/presence",
		"sendMessage",
		"markRead",
		"sendReaction",
		"setPresence",
	}

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "data" {
				return filepath.SkipDir
			}
			return nil
		}

		// Only check Go source files, excluding audit test itself and DECISIONS/README documentation
		if !strings.HasSuffix(path, ".go") || strings.Contains(path, "readonly_audit_test.go") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contentStr := string(content)

		for _, pattern := range prohibitedPatterns {
			if strings.Contains(contentStr, pattern) {
				t.Errorf("CRITICAL READ-ONLY VIOLATION: prohibited pattern %q found in %s", pattern, path)
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("failed to scan codebase: %v", err)
	}
}

// TestAudit_OmniRouteNoTools verifies that OmniRoute requests never contain tools, functions, or tool_choice.
func TestAudit_OmniRouteNoTools(t *testing.T) {
	var requestBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&requestBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant", "content": "{\"category\":\"info_umum\",\"importance\":1,\"action_required\":false,\"summary\":\"Info\",\"confidence\":0.9}"}}]
		}`))
	}))
	defer server.Close()

	client := omniroute.NewClient(server.URL, "key", "model")
	_, err := client.ClassifyMessage(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, forbiddenField := range []string{"tools", "functions", "tool_choice"} {
		if _, exists := requestBody[forbiddenField]; exists {
			t.Errorf("CRITICAL SECURITY AUDIT FAILURE: OmniRoute request contains %q", forbiddenField)
		}
	}
}

// TestAudit_DockerComposeHardening verifies compose configuration forbids external ports and enforces read-only.
func TestAudit_DockerComposeHardening(t *testing.T) {
	composeBytes, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatalf("failed to read docker-compose.yml: %v", err)
	}
	composeStr := string(composeBytes)

	// 1. Verify GOWA ports are NOT published to host
	// Ensure no active lines mapping gowa ports outside
	lines := strings.Split(composeStr, "\n")
	inGowa := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "gowa:") {
			inGowa = true
			continue
		}
		if inGowa && strings.HasPrefix(trimmed, "analyzer:") {
			inGowa = false
		}
		if inGowa && strings.HasPrefix(trimmed, "ports:") {
			t.Errorf("CRITICAL SECURITY AUDIT FAILURE: GOWA service has active ports exposed in docker-compose.yml: %s", line)
		}
	}

	// 2. Verify mandatory privacy & read-only env variables for GOWA
	mandatorySettings := []string{
		"MCP_ENABLED=false",
		"WHATSAPP_PRESENCE_PULSE_ENABLED=false",
		"WHATSAPP_PRESENCE_ON_CONNECT=unavailable",
		"WHATSAPP_AUTO_MARK_READ=false",
		"WHATSAPP_AUTO_REJECT_CALL=false",
		"WHATSAPP_WEBHOOK_EVENTS=message",
	}

	for _, setting := range mandatorySettings {
		if !strings.Contains(composeStr, setting) {
			t.Errorf("CRITICAL SECURITY AUDIT FAILURE: docker-compose.yml missing mandatory privacy setting: %s", setting)
		}
	}
}
