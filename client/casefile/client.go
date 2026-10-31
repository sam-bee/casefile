// Package casefile provides a small JSON document client for the Casefile POST API.
package casefile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Project string
	Actor   string
	HTTP    *http.Client
}

func New(baseURL, project, actor string) *Client {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8090/api"
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Project: project, Actor: actor,
		HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Post sends one JSON object to an API operation and returns the response document.
// Operation is a path such as "casenotes/create". Project and actor are filled in
// when absent, so callers can pass through the same JSON they store in research logs.
func (c *Client) Post(ctx context.Context, operation string, document json.RawMessage) (json.RawMessage, error) {
	operation = strings.TrimPrefix(operation, "/api/")
	operation = strings.TrimPrefix(operation, "/")
	if operation == "" || strings.Contains(operation, "..") || strings.ContainsAny(operation, "?#") {
		return nil, fmt.Errorf("invalid operation %q", operation)
	}
	endpoint, err := url.Parse(c.BaseURL + "/" + operation)
	if err != nil {
		return nil, err
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("unsupported API URL scheme")
	}
	if len(document) == 0 {
		document = json.RawMessage(`{}`)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(document, &fields); err != nil {
		return nil, fmt.Errorf("JSON object required: %w", err)
	}
	if fields == nil {
		return nil, fmt.Errorf("JSON object required")
	}
	if c.Project != "" && operation != "projects/list" && operation != "projects/create" && operation != "health" {
		if _, present := fields["project"]; !present {
			if _, idPresent := fields["project_id"]; !idPresent {
				fields["project"], _ = json.Marshal(c.Project)
			}
		}
	}
	if c.Actor != "" {
		key := ""
		switch operation {
		case "casenotes/create", "comments/create", "links/create":
			key = "author"
		case "casenotes/update":
			key = "actor"
		}
		if key != "" {
			if _, present := fields[key]; !present {
				fields[key], _ = json.Marshal(c.Actor)
			}
		}
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Casefile %s: HTTP %d: %s", operation, response.StatusCode, strings.TrimSpace(string(answer)))
	}
	if !json.Valid(answer) {
		return nil, fmt.Errorf("Casefile returned invalid JSON")
	}
	return json.RawMessage(answer), nil
}

func (c *Client) EnsureProject(ctx context.Context, slug, name string) (json.RawMessage, error) {
	if slug == "" {
		slug = c.Project
	}
	if slug == "" {
		return nil, fmt.Errorf("project slug is required")
	}
	result, err := c.Post(ctx, "projects/list", nil)
	if err != nil {
		return nil, err
	}
	var listing struct {
		Projects []struct {
			Slug string `json:"slug"`
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(result, &listing); err != nil {
		return nil, err
	}
	for _, project := range listing.Projects {
		if project.Slug == slug {
			return json.Marshal(map[string]any{"project": project, "existing": true})
		}
	}
	if name == "" {
		name = slug
	}
	payload, _ := json.Marshal(map[string]string{"slug": slug, "name": name})
	return c.Post(ctx, "projects/create", payload)
}
