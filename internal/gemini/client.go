package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Gemini client with optional proxy and custom base URL.
func NewClient(apiKey, model, baseURL, proxyURL string) *Client {
	if model == "" {
		model = "gemini-3.8-flash"
	}
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	if proxyURL != "" {
		if parsedProxy, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(parsedProxy)
		}
	}

	return &Client{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   45 * time.Second,
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

// GenerateAnswer requests a response with automatic multi-model failover.
// If one free model experiences 503 high demand, 429 rate limit, or 404, it immediately tries the next candidate.
func (c *Client) GenerateAnswer(ctx context.Context, systemPrompt string, userPrompt string, useSearch bool) (string, error) {
	// Candidate pool of free Flash models verified to work on free tier (gemini-3.5-flash is rock solid)
	rawCandidates := []string{
		"gemini-3.5-flash",
		"gemini-3.6-flash",
		"gemini-3.5-flash-lite",
		"gemini-flash-lite-latest",
		"gemini-flash-latest",
		c.model,
	}

	// Deduplicate while preserving order
	seen := make(map[string]bool)
	var candidateModels []string
	for _, m := range rawCandidates {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			candidateModels = append(candidateModels, m)
		}
	}

	var lastErr error
	for _, modelName := range candidateModels {
		// Each model attempt gets up to 12s so one slow model never starves the others
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, 12*time.Second)

		// Attempt 1: With Search Grounding (if requested)
		resp, err := c.callAPI(attemptCtx, modelName, systemPrompt, userPrompt, useSearch)
		cancelAttempt()

		if err == nil {
			log.Printf("[Gemini Success] Successfully generated response using model %s", modelName)
			return resp, nil
		}

		lastErr = err

		// If failed with search enabled, immediately attempt same model without search (fast fallback)
		if useSearch {
			noSearchCtx, cancelNoSearch := context.WithTimeout(ctx, 10*time.Second)
			fallbackResp, fallbackErr := c.callAPI(noSearchCtx, modelName, systemPrompt, userPrompt, false)
			cancelNoSearch()

			if fallbackErr == nil {
				log.Printf("[Gemini Success] Successfully generated response using model %s (without search tool)", modelName)
				return fallbackResp, nil
			}
			lastErr = fallbackErr
		}

		log.Printf("[Gemini Fallback] Model %s unavailable (%v), failing over to next free model...", modelName, lastErr)
	}

	if errors.Is(lastErr, ErrQuotaExhausted) {
		return "", ErrQuotaExhausted
	}
	return "", lastErr
}

func (c *Client) callAPI(ctx context.Context, modelName string, systemPrompt string, userPrompt string, useSearch bool) (string, error) {
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.baseURL, modelName, c.apiKey)

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
