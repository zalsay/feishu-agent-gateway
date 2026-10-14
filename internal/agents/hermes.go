package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"feishu-agent-gateway/internal/gateway"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type Hermes struct {
	base   string
	key    string
	model  string
	client *http.Client
}

func NewHermes(base, key, model string, client *http.Client) (*Hermes, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || key == "" || model == "" {
		return nil, errors.New("invalid Hermes configuration")
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("Hermes requires HTTPS for non-loopback addresses")
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Hermes{base: strings.TrimRight(base, "/"), key: key, model: model, client: client}, nil
}
func (h *Hermes) Models() []string  { return []string{h.model} }
func (h *Hermes) Efforts() []string { return []string{"low", "medium", "high", "xhigh"} }
func (h *Hermes) Run(ctx context.Context, prompt, continuation, scope string) (string, string, error) {
	return h.RunWithOptions(ctx, prompt, continuation, scope, gateway.RunOptions{Reasoning: "high"})
}
func (h *Hermes) RunWithOptions(ctx context.Context, prompt, continuation, scope string, opts gateway.RunOptions) (string, string, error) {
	if opts.Reasoning == "" {
		opts.Reasoning = "high"
	}
	if !allowedOption(h.Efforts(), opts.Reasoning) || opts.Model != "" && !allowedOption(h.Models(), opts.Model) {
		return "", "", errors.New("unsupported Hermes model or reasoning effort")
	}

	if continuation != "" {
		check, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+"/v1/responses/"+url.PathEscape(continuation), nil)
		if err != nil {
			return "", "", err
		}
		check.Header.Set("Authorization", "Bearer "+h.key)
		check.Header.Set("X-Hermes-Session-Key", scope)
		res, err := h.client.Do(check)
		if err != nil {
			return "", "", fmt.Errorf("check Hermes response: %w", err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return "", "", gateway.ErrInvalidContinuation
		}
		if res.StatusCode != http.StatusOK {
			return "", "", fmt.Errorf("Hermes response check returned HTTP %d", res.StatusCode)
		}
	}
	model := h.model
	if opts.Model != "" {
		model = opts.Model
	}
	payload := map[string]any{"model": model, "input": prompt, "store": true, "model_options": map[string]string{"reasoning_effort": opts.Reasoning}}
	if continuation != "" {
		payload["previous_response_id"] = continuation
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+"/v1/responses", bytes.NewReader(data))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("X-Hermes-Session-Key", scope)
	res, err := h.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("Hermes request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound && continuation != "" {
		return "", "", gateway.ErrInvalidContinuation
	}
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("Hermes returned HTTP %d", res.StatusCode)
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&out); err != nil {
		return "", "", fmt.Errorf("decode Hermes response: %w", err)
	}
	if out.Status != "completed" || out.ID == "" {
		return "", "", fmt.Errorf("Hermes response did not complete: %s", out.Status)
	}
	var text strings.Builder
	for _, item := range out.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", "", errors.New("Hermes returned no text")
	}
	return text.String(), out.ID, nil
}
