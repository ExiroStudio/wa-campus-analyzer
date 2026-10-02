package gowaclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type DeviceRecord struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	JID       string `json:"jid,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type DeviceStatus struct {
	ID        string `json:"id"`
	State     string `json:"state"` // "connected", "disconnected", "unpaired"
	JID       string `json:"jid,omitempty"`
	Connected bool   `json:"connected"`
}

type Client struct {
	baseURL       string
	basicAuthUser string
	basicAuthPass string
	httpClient    *http.Client
}

func NewClient(baseURL, basicAuthUser, basicAuthPass string) *Client {
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		basicAuthUser: basicAuthUser,
		basicAuthPass: basicAuthPass,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

type gowaListDevicesResponse struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Results []DeviceRecord `json:"results"`
}

type gowaAddDeviceResponse struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Results DeviceRecord `json:"results"`
}

type gowaLoginResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Results struct {
		DeviceID   string `json:"device_id"`
		QRDuration int    `json:"qr_duration"`
		QRLink     string `json:"qr_link"`
	} `json:"results"`
}

func (c *Client) GetDeviceStatus(ctx context.Context) (*DeviceStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/devices", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.SetBasicAuth(c.basicAuthUser, c.basicAuthPass)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GOWA connection error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GOWA returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var listResp gowaListDevicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("failed to decode devices response: %w", err)
	}

	if len(listResp.Results) == 0 {
		return &DeviceStatus{
			State:     "unpaired",
			Connected: false,
		}, nil
	}

	primary := listResp.Results[0]
	// Sebuah device GOWA terhubung jika statusnya "logged_in" atau sudah memiliki JID akun WhatsApp.
	hasValidJID := strings.TrimSpace(primary.JID) != "" && strings.Contains(primary.JID, "@")
	isLoggedInState := strings.EqualFold(primary.State, "logged_in")
	isConnected := isLoggedInState || (strings.EqualFold(primary.State, "connected") && hasValidJID)

	state := primary.State
	if isConnected {
		state = "logged_in"
	} else if !hasValidJID {
		state = "unpaired"
	}

	return &DeviceStatus{
		ID:        primary.ID,
		State:     state,
		JID:       primary.JID,
		Connected: isConnected,
	}, nil
}

func (c *Client) GetQRCodePNG(ctx context.Context) ([]byte, int, error) {
	status, err := c.GetDeviceStatus(ctx)
	if err != nil {
		return nil, 0, err
	}

	if status.Connected || status.State == "logged_in" || (status.JID != "" && strings.Contains(status.JID, "@")) {
		return nil, 0, errors.New("WhatsApp sudah terhubung")
	}

	deviceID := status.ID
	if deviceID == "" {
		// Buat placeholder device baru jika belum ada
		createdID, err := c.createDevice(ctx)
		if err != nil {
			return nil, 0, fmt.Errorf("gagal membuat perangkat baru di GOWA: %w", err)
		}
		deviceID = createdID
	}

	// Minta QR login
	loginReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/devices/%s/login", c.baseURL, deviceID), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create login request: %w", err)
	}
	loginReq.SetBasicAuth(c.basicAuthUser, c.basicAuthPass)

	loginResp, err := c.httpClient.Do(loginReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to request login QR: %w", err)
	}
	defer loginResp.Body.Close()

	respBody, _ := io.ReadAll(loginResp.Body)
	if strings.Contains(strings.ToLower(string(respBody)), "already logged in") {
		return nil, 0, errors.New("WhatsApp sudah terhubung")
	}

	if loginResp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("GOWA login returned HTTP %d: %s", loginResp.StatusCode, string(respBody))
	}

	var loginData gowaLoginResponse
	if err := json.Unmarshal(respBody, &loginData); err != nil {
		return nil, 0, fmt.Errorf("failed to decode login response: %w", err)
	}

	qrDuration := loginData.Results.QRDuration
	if qrDuration <= 0 {
		qrDuration = 30
	}

	// Unduh gambar QR PNG dari GOWA
	imgURL := loginData.Results.QRLink
	if imgURL == "" {
		return nil, 0, errors.New("GOWA tidak mengembalikan link gambar QR")
	}

	parsedURL, err := url.Parse(imgURL)
	var fetchURL string
	if err == nil && parsedURL.Path != "" {
		fetchURL = c.baseURL + parsedURL.Path
	} else {
		fetchURL = imgURL
	}

	imgReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create image request: %w", err)
	}
	imgReq.SetBasicAuth(c.basicAuthUser, c.basicAuthPass)

	imgResp, err := c.httpClient.Do(imgReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to download QR image: %w", err)
	}
	defer imgResp.Body.Close()

	if imgResp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("GOWA image returned HTTP %d", imgResp.StatusCode)
	}

	imgBytes, err := io.ReadAll(imgResp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read QR image bytes: %w", err)
	}

	return imgBytes, qrDuration, nil
}

func (c *Client) DeleteDevice(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/devices", nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.basicAuthUser, c.basicAuthPass)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var listResp gowaListDevicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil
	}

	for _, d := range listResp.Results {
		delReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/devices/%s", c.baseURL, d.ID), nil)
		if err != nil {
			continue
		}
		delReq.SetBasicAuth(c.basicAuthUser, c.basicAuthPass)
		delResp, err := c.httpClient.Do(delReq)
		if err == nil {
			delResp.Body.Close()
		}
	}

	return nil
}

func (c *Client) createDevice(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/devices", bytes.NewBufferString("{}"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.basicAuthUser, c.basicAuthPass)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var addResp gowaAddDeviceResponse
	if err := json.NewDecoder(resp.Body).Decode(&addResp); err != nil {
		return "", err
	}

	if addResp.Results.ID == "" {
		return "", errors.New("empty device ID returned by GOWA")
	}

	return addResp.Results.ID, nil
}
