package omniroute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestParseAndValidateResponse(t *testing.T) {
	// Case 1: Code fences and extra text
	rawWithFence := "Here is your analysis:\n```json\n{\n  \"category\": \"tugas\",\n  \"importance\": 4,\n  \"action_required\": true,\n  \"deadline\": \"2026-10-06T10:00:00+07:00\",\n  \"event_start\": null,\n  \"event_location\": null,\n  \"topic\": \"Pemrograman Web\",\n  \"summary\": \"Kumpulkan tugas jobsheet 5\",\n  \"reasoning\": \"Tugas dengan deadline jelas\",\n  \"confidence\": 0.95\n}\n```\nHope this helps!"

	res, err := ParseAndValidateResponse(rawWithFence)
	if err != nil {
		t.Fatalf("expected successful parsing, got %v", err)
	}
	if res.Category != "tugas" {
		t.Errorf("expected tugas, got %s", res.Category)
	}
	if res.Importance != 4 {
		t.Errorf("expected importance 4, got %d", res.Importance)
	}
	if res.Deadline == nil || *res.Deadline != "2026-10-06T03:00:00Z" {
		t.Errorf("expected UTC deadline '2026-10-06T03:00:00Z', got %v", res.Deadline)
	}
	if res.NeedsReview != 0 {
		t.Errorf("expected needs_review 0, got %d", res.NeedsReview)
	}

	// Case 2: Invalid category
	badCategory := `{"category":"unknown","importance":3,"action_required":false,"summary":"test","confidence":0.8}`
	_, err = ParseAndValidateResponse(badCategory)
	if err == nil {
		t.Errorf("expected error for invalid category, got nil")
	}

	// Case 3: Missing deadline for task triggers needs_review
	taskNoDeadline := `{"category":"tugas","importance":3,"action_required":true,"summary":"Kerjakan tugas","confidence":0.9}`
	res3, err := ParseAndValidateResponse(taskNoDeadline)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res3.NeedsReview != 1 {
		t.Errorf("expected needs_review=1 when deadline is missing for task, got %d", res3.NeedsReview)
	}

	// Case 4: Low confidence triggers needs_review
	lowConf := `{"category":"info_umum","importance":1,"action_required":false,"summary":"Info umum","confidence":0.4}`
	res4, err := ParseAndValidateResponse(lowConf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res4.NeedsReview != 1 {
		t.Errorf("expected needs_review=1 when confidence < 0.6, got %d", res4.NeedsReview)
	}
}

func TestClient_NoToolsAndHeaderCheck(t *testing.T) {
	var capturedBody map[string]any
	var capturedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant", "content": "{\"category\":\"tugas\",\"importance\":3,\"action_required\":true,\"summary\":\"Tugas\",\"confidence\":0.9,\"deadline\":\"2026-10-10T12:00:00Z\"}"}}],
			"usage": {"prompt_tokens": 50, "completion_tokens": 30}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-api-key", "gpt-4o-mini")
	resp, err := client.ClassifyMessage(context.Background(), "sys prompt", "user prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedAuth != "Bearer secret-api-key" {
		t.Errorf("expected Bearer secret-api-key, got %s", capturedAuth)
	}

	// AUDIT TEST: Ensure NO tools, functions, or tool_choice
	if _, ok := capturedBody["tools"]; ok {
		t.Errorf("CRITICAL SECURITY VIOLATION: request body contains 'tools'")
	}
	if _, ok := capturedBody["functions"]; ok {
		t.Errorf("CRITICAL SECURITY VIOLATION: request body contains 'functions'")
	}
	if _, ok := capturedBody["tool_choice"]; ok {
		t.Errorf("CRITICAL SECURITY VIOLATION: request body contains 'tool_choice'")
	}

	if resp.TokensIn != 50 || resp.TokensOut != 30 {
		t.Errorf("expected 50 in, 30 out; got %d, %d", resp.TokensIn, resp.TokensOut)
	}
}

func TestClient_BadJSONRetry(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")

		if count == 1 {
			// First response is broken JSON
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "Here is result: { broken json ..."}}]}`))
			return
		}

		// Second response is fixed JSON
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "{\"category\":\"pengumuman\",\"importance\":2,\"action_required\":false,\"summary\":\"Libur kampus\",\"confidence\":0.95}"}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", "gpt-4o-mini")
	resp, err := client.ClassifyMessage(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}

	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
	if resp.Result.Category != "pengumuman" {
		t.Errorf("expected pengumuman, got %s", resp.Result.Category)
	}
}

func TestClient_ResponseFormatFallback(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")

		if count == 1 {
			// Reject with 400 response_format error
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": "response_format is not supported by this model"}`))
			return
		}

		// Succeeded without response_format
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "{\"category\":\"jadwal\",\"importance\":3,\"action_required\":true,\"summary\":\"Jadwal kuliah\",\"confidence\":0.9}"}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", "gpt-4o-mini")
	resp, err := client.ClassifyMessage(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("expected fallback to succeed, got %v", err)
	}

	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
	if resp.Result.Category != "jadwal" {
		t.Errorf("expected jadwal, got %s", resp.Result.Category)
	}
}
