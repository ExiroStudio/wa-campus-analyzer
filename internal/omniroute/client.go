package omniroute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

var ValidCategories = map[string]struct{}{
	"tugas":          {},
	"ujian_kuis":     {},
	"workshop_event": {},
	"jadwal":         {},
	"pengumuman":     {},
	"info_umum":      {},
	"obrolan":        {},
	"spam":           {},
}

type Client struct {
	baseURL             string
	apiKey              string
	model               string
	httpClient          *http.Client
	disableJSONResponse atomic.Bool
}

func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionRequest struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type ClassificationResult struct {
	Category       string  `json:"category"`
	Importance     int     `json:"importance"`
	ActionRequired bool    `json:"action_required"`
	Deadline       *string `json:"deadline"`
	EventStart     *string `json:"event_start"`
	EventLocation  *string `json:"event_location"`
	Topic          *string `json:"topic"`
	Summary        string  `json:"summary"`
	Reasoning      string  `json:"reasoning"`
	Confidence     float64 `json:"confidence"`
	NeedsReview    int     `json:"-"`
}

type AnalysisResponse struct {
	Result      *ClassificationResult
	RawResponse string
	TokensIn    int
	TokensOut   int
	Model       string
}

// ClassifyMessage performs classification with schema validation and single retry.
func (c *Client) ClassifyMessage(ctx context.Context, systemPrompt, userPrompt string) (*AnalysisResponse, error) {
	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	rawResp, tokensIn, tokensOut, err := c.callChatCompletion(ctx, messages)
	if err != nil {
		return nil, err
	}

	result, valErr := ParseAndValidateResponse(rawResp)
	if valErr == nil {
		return &AnalysisResponse{
			Result:      result,
			RawResponse: rawResp,
			TokensIn:    tokensIn,
			TokensOut:   tokensOut,
			Model:       c.model,
		}, nil
	}

	// Retry once with corrective guidance
	messages = append(messages,
		ChatMessage{Role: "assistant", Content: rawResp},
		ChatMessage{Role: "user", Content: "Balasan sebelumnya bukan JSON valid sesuai skema. Balas hanya satu JSON object sesuai skema."},
	)

	rawResp2, tokensIn2, tokensOut2, err2 := c.callChatCompletion(ctx, messages)
	if err2 != nil {
		return nil, fmt.Errorf("retry failed: %w (initial validation error: %v)", err2, valErr)
	}

	result2, valErr2 := ParseAndValidateResponse(rawResp2)
	if valErr2 != nil {
		return nil, fmt.Errorf("second validation failed: %w (response: %s)", valErr2, rawResp2)
	}

	return &AnalysisResponse{
		Result:      result2,
		RawResponse: rawResp2,
		TokensIn:    tokensIn + tokensIn2,
		TokensOut:   tokensOut + tokensOut2,
		Model:       c.model,
	}, nil
}

func (c *Client) callChatCompletion(ctx context.Context, messages []ChatMessage) (string, int, int, error) {
	useJSONFormat := !c.disableJSONResponse.Load()

	reqBody := ChatCompletionRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0,
		MaxTokens:   500,
	}
	if useJSONFormat {
		reqBody.ResponseFormat = &ResponseFormat{Type: "json_object"}
	}

	resp, respBody, err := c.doRequest(ctx, reqBody)
	if err != nil {
		return "", 0, 0, err
	}

	// Check if 400 caused by unsupported response_format
	if resp.StatusCode == http.StatusBadRequest && useJSONFormat && strings.Contains(strings.ToLower(string(respBody)), "response_format") {
		c.disableJSONResponse.Store(true)
		reqBody.ResponseFormat = nil
		resp, respBody, err = c.doRequest(ctx, reqBody)
		if err != nil {
			return "", 0, 0, err
		}
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", 0, 0, &RateLimitError{Message: string(respBody)}
	}
	if resp.StatusCode >= 500 {
		return "", 0, 0, &ServerError{StatusCode: resp.StatusCode, Message: string(respBody)}
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, 0, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", 0, 0, fmt.Errorf("failed to unmarshal completion response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", 0, 0, errors.New("empty choices in completion response")
	}

	content := chatResp.Choices[0].Message.Content
	return content, chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens, nil
}

func (c *Client) doRequest(ctx context.Context, payload ChatCompletionRequest) (*http.Response, []byte, error) {
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("network error during request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return resp, bodyBytes, nil
}

// ParseAndValidateResponse extracts JSON from text and strictly validates schema.
func ParseAndValidateResponse(rawText string) (*ClassificationResult, error) {
	cleanJSON := extractJSON(rawText)
	if cleanJSON == "" {
		return nil, errors.New("no valid JSON object found in response")
	}

	var res ClassificationResult
	if err := json.Unmarshal([]byte(cleanJSON), &res); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}

	// 1. Category validation
	res.Category = strings.ToLower(strings.TrimSpace(res.Category))
	if _, ok := ValidCategories[res.Category]; !ok {
		return nil, fmt.Errorf("invalid category '%s'", res.Category)
	}

	// 2. Importance validation
	if res.Importance < 1 || res.Importance > 5 {
		return nil, fmt.Errorf("importance %d out of range (1-5)", res.Importance)
	}

	// 3. Confidence validation
	if res.Confidence < 0.0 || res.Confidence > 1.0 {
		return nil, fmt.Errorf("confidence %f out of range (0.0-1.0)", res.Confidence)
	}

	// 4. Summary validation
	res.Summary = strings.TrimSpace(res.Summary)
	if res.Summary == "" {
		return nil, errors.New("summary cannot be empty")
	}
	if len([]rune(res.Summary)) > 160 {
		res.Summary = string([]rune(res.Summary)[:160])
	}

	// 5. Date validation and UTC conversion
	needsReview := 0
	if res.Deadline != nil && strings.TrimSpace(*res.Deadline) != "" {
		utcStr, err := parseToUTC(*res.Deadline)
		if err != nil {
			res.Deadline = nil
			needsReview = 1
		} else {
			res.Deadline = &utcStr
		}
	} else {
		res.Deadline = nil
	}

	if res.EventStart != nil && strings.TrimSpace(*res.EventStart) != "" {
		utcStr, err := parseToUTC(*res.EventStart)
		if err != nil {
			res.EventStart = nil
			needsReview = 1
		} else {
			res.EventStart = &utcStr
		}
	} else {
		res.EventStart = nil
	}

	// 6. Check needs_review criteria:
	// - confidence < 0.6
	// - kategori tugas/workshop_event/ujian_kuis tetapi deadline/event_start kosong
	if res.Confidence < 0.6 {
		needsReview = 1
	}
	if (res.Category == "tugas" || res.Category == "workshop_event" || res.Category == "ujian_kuis") &&
		res.Deadline == nil && res.EventStart == nil {
		needsReview = 1
	}

	res.NeedsReview = needsReview
	return &res, nil
}

func parseToUTC(dateStr string) (string, error) {
	dateStr = strings.TrimSpace(dateStr)
	// Try standard ISO formats
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("unable to parse date '%s'", dateStr)
}

func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	// Remove markdown fences
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		var filtered []string
		for _, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "```") {
				continue
			}
			filtered = append(filtered, l)
		}
		s = strings.Join(filtered, "\n")
	}

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start != -1 && end != -1 && end > start {
		return s[start : end+1]
	}
	return ""
}

type RateLimitError struct {
	Message string
}

func (e *RateLimitError) Error() string {
	return "rate limited (429): " + e.Message
}

type ServerError struct {
	StatusCode int
	Message    string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("server error (%d): %s", e.StatusCode, e.Message)
}
