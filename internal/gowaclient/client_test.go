package gowaclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGOWAClient_GetDeviceStatus_Unpaired(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devices" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    "SUCCESS",
				"message": "List devices",
				"results": []any{},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "admin", "pass")
	status, err := client.GetDeviceStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.Connected {
		t.Errorf("expected disconnected/unpaired, got connected")
	}
	if status.State != "unpaired" {
		t.Errorf("expected unpaired state, got %s", status.State)
	}
}

func TestGOWAClient_GetDeviceStatus_Connected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devices" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    "SUCCESS",
				"message": "List devices",
				"results": []map[string]any{
					{
						"id":    "dev-123",
						"state": "connected",
						"jid":   "62812345678@s.whatsapp.net",
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "admin", "pass")
	status, err := client.GetDeviceStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !status.Connected {
		t.Errorf("expected connected, got disconnected")
	}
	if status.ID != "dev-123" {
		t.Errorf("expected dev-123, got %s", status.ID)
	}
	if status.JID != "62812345678@s.whatsapp.net" {
		t.Errorf("expected jid, got %s", status.JID)
	}
}

func TestGOWAClient_GetQRCodePNG(t *testing.T) {
	fakePNG := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devices":
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code":    "SUCCESS",
					"message": "List devices",
					"results": []map[string]any{
						{
							"id":    "dev-456",
							"state": "disconnected",
						},
					},
				})
			}
		case "/devices/dev-456/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    "SUCCESS",
				"message": "Login success",
				"results": map[string]any{
					"device_id":   "dev-456",
					"qr_duration": 25,
					"qr_link":     "/statics/qrcode/test.png",
				},
			})
		case "/statics/qrcode/test.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(fakePNG)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "admin", "pass")
	qrBytes, duration, err := client.GetQRCodePNG(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if duration != 25 {
		t.Errorf("expected duration 25, got %d", duration)
	}
	if len(qrBytes) != len(fakePNG) {
		t.Errorf("expected %d bytes, got %d", len(fakePNG), len(qrBytes))
	}
}

func TestGOWAClient_GetDeviceStatus_ConnectedStateWithoutJID_IsUnpaired(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devices" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    "SUCCESS",
				"message": "List devices",
				"results": []map[string]any{
					{
						"id":    "placeholder-uuid",
						"state": "connected",
						"jid":   "",
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "admin", "pass")
	status, err := client.GetDeviceStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.Connected {
		t.Errorf("expected Connected to be false when JID is empty, got true")
	}
	if status.State != "unpaired" {
		t.Errorf("expected State to be 'unpaired', got '%s'", status.State)
	}
}

