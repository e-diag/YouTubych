package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrQuotaExhausted indicates Gemini API free tier rate limit or quota has been reached.
	ErrQuotaExhausted = errors.New("gemini quota or rate limit exhausted")
	// ErrNoResponse indicates model produced an empty response or was filtered.
	ErrNoResponse = errors.New("no response candidate returned from gemini")
)

type Client struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewClient creates a new Gemini client.
func NewClient(apiKey, model string) *Client {
	if model == "" {
		model = "gemini-3.8-flash"
	}
	return &Client{
		apiKey: apiKey,
		model:  model,
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

// Request and Response structs for Gemini REST API v1beta
type requestPayload struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"system_instruction,omitempty"`
	Tools             []tool            `json:"tools,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text"`
}

type tool struct {
	GoogleSearch *googleSearch `json:"google_search,omitempty"`
}

type googleSearch struct{}

type generationConfig struct {
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

type responsePayload struct {
	Candidates []struct {
		Content struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason      string             `json:"finishReason"`
		GroundingMetadata *groundingMetadata `json:"groundingMetadata,omitempty"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

type groundingMetadata struct {
	WebSearchQueries []string `json:"webSearchQueries,omitempty"`
	GroundingChunks  []struct {
		Web struct {
			URI   string `json:"uri"`
			Title string `json:"title"`
		} `json:"web"`
	} `json:"groundingChunks"`
}

// GenerateAnswer requests a response with optional Google Search Grounding.
// If grounding fails or quota is exhausted, it attempts fallback without tools.
func (c *Client) GenerateAnswer(ctx context.Context, systemPrompt string, userPrompt string, useSearch bool) (string, error) {
	resp, err := c.callAPI(ctx, systemPrompt, userPrompt, useSearch)
	if err != nil {
		// If failed with search tool enabled, try fallback without search
		if useSearch {
			fallbackResp, fallbackErr := c.callAPI(ctx, systemPrompt, userPrompt, false)
			if fallbackErr == nil {
				return fallbackResp, nil
			}
			// If fallback also failed due to quota
			if errors.Is(fallbackErr, ErrQuotaExhausted) {
				return "", ErrQuotaExhausted
			}
		}
		return "", err
	}
	return resp, nil
}

func (c *Client) callAPI(ctx context.Context, systemPrompt string, userPrompt string, useSearch bool) (string, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", c.model, c.apiKey)

	reqData := requestPayload{
		Contents: []content{
			{
				Role:  "user",
				Parts: []part{{Text: userPrompt}},
			},
		},
		GenerationConfig: &generationConfig{
			Temperature:     0.7,
			MaxOutputTokens: 800,
		},
	}

	if systemPrompt != "" {
		reqData.SystemInstruction = &content{
			Parts: []part{{Text: systemPrompt}},
		}
	}

	if useSearch {
		reqData.Tools = []tool{
			{GoogleSearch: &googleSearch{}},
		}
	}

	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal gemini request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("gemini http request failed: %w", err)
	}
	defer httpResp.Body.Close()

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read gemini response body: %w", err)
	}

	if httpResp.StatusCode == http.StatusTooManyRequests {
		return "", ErrQuotaExhausted
	}

	var resPayload responsePayload
	if err := json.Unmarshal(bodyBytes, &resPayload); err != nil {
		return "", fmt.Errorf("failed to decode gemini response (%d): %s", httpResp.StatusCode, string(bodyBytes))
	}

	if resPayload.Error != nil {
		if resPayload.Error.Code == 429 || strings.Contains(resPayload.Error.Status, "RESOURCE_EXHAUSTED") {
			return "", ErrQuotaExhausted
		}
		return "", fmt.Errorf("gemini api error [%d - %s]: %s", resPayload.Error.Code, resPayload.Error.Status, resPayload.Error.Message)
	}

	if len(resPayload.Candidates) == 0 {
		return "", ErrNoResponse
	}

	candidate := resPayload.Candidates[0]
	var textBuilder strings.Builder
	for _, p := range candidate.Content.Parts {
		textBuilder.WriteString(p.Text)
	}

	result := strings.TrimSpace(textBuilder.String())
	if result == "" {
		return "", ErrNoResponse
	}

	return result, nil
}
