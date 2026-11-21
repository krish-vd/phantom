// Package llm talks to local models: Ollama's native API, or any
// OpenAI-compatible server (LM Studio, llama.cpp server, vLLM, LocalAI).
// No API key is needed for local servers; one is sent only if provided.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	Provider string // "ollama" or "openai"
	Endpoint string // base URL
	Model    string
	APIKey   string  // optional, for hosted OpenAI-compatible endpoints
	Temp     float64 // sampling temperature
	HTTP     *http.Client
}

const system = "You are a helpful senior software engineer. Answer with working code. " +
	"Always include the shell command to install any third-party packages you use."

func New(provider, endpoint, model, apiKey string, temp float64) (*Client, error) {
	provider = strings.ToLower(provider)
	if endpoint == "" {
		switch provider {
		case "ollama":
			endpoint = "http://localhost:11434"
		case "openai":
			endpoint = "http://localhost:1234/v1"
		}
	}
	if provider != "ollama" && provider != "openai" {
		return nil, fmt.Errorf("unknown provider %q (use ollama or openai)", provider)
	}
	if model == "" {
		return nil, fmt.Errorf("--model is required")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	return &Client{Provider: provider, Endpoint: strings.TrimRight(endpoint, "/"), Model: model,
		APIKey: apiKey, Temp: temp, HTTP: &http.Client{Timeout: 10 * time.Minute}}, nil
}

// Ask sends one prompt and returns the model's text answer.
func (c *Client) Ask(ctx context.Context, prompt string) (string, error) {
	msgs := []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": prompt}}
	if c.Provider == "ollama" {
		var out struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Error string `json:"error"`
		}
		err := c.post(ctx, c.Endpoint+"/api/chat", map[string]any{
			"model": c.Model, "messages": msgs, "stream": false,
			"options": map[string]any{"temperature": c.Temp},
		}, &out)
		if err == nil && out.Error != "" {
			err = fmt.Errorf("ollama: %s", out.Error)
		}
		return out.Message.Content, err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.post(ctx, c.Endpoint+"/chat/completions", map[string]any{
		"model": c.Model, "messages": msgs, "temperature": c.Temp,
	}, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("model returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}

func (c *Client) post(ctx context.Context, url string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") && c.Provider == "ollama" {
			return fmt.Errorf("can't reach Ollama at %s. Is it running? (ollama serve)", c.Endpoint)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("%s: %s: %s", url, resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
