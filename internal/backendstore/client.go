// Package backendstore provides the typed HTTP store client without a local DB.
package backendstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/marcus/td/internal/config"
)

type Client struct {
	URL, Token string
	HTTP       *http.Client
}
type Project struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	TitleMinLength int    `json:"title_min_length"`
	TitleMaxLength int    `json:"title_max_length"`
}

func NewClient(endpoint, token string) (*Client, error) {
	endpoint, e := config.NormalizeBackendURL(endpoint)
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, fmt.Errorf("TD_BACKEND_TOKEN is required and must not contain whitespace")
	}
	return &Client{URL: endpoint, Token: token, HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) request(ctx context.Context, method, path, session, key string, body any, out any) error {
	return c.requestWithMatch(ctx, method, path, session, key, "", body, out)
}
func (c *Client) requestWithMatch(ctx context.Context, method, path, session, key, match string, body any, out any) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	var data []byte
	var e error
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil {
			return e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(data))
	if e != nil {
		return fmt.Errorf("invalid backend request")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if session != "" {
		req.Header.Set("X-TD-Session", session)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if match != "" {
		req.Header.Set("If-Match", match)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, e := c.HTTP.Do(req)
	if e != nil {
		return requestFailure(method, key, fmt.Errorf("backend request failed: %w", e))
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if e != nil {
		return requestFailure(method, key, fmt.Errorf("read backend response: %w", e))
	}
	if len(raw) > 4<<20 {
		return requestFailure(method, key, fmt.Errorf("backend response exceeds limit"))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		e := &APIError{StatusCode: response.StatusCode, Code: statusCode(response.StatusCode), Method: method, Path: path}
		if response.StatusCode >= 500 {
			return requestFailure(method, key, e)
		}
		return e
	}
	var envelope struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if e = json.Unmarshal(raw, &envelope); e != nil || !envelope.OK || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return requestFailure(method, key, fmt.Errorf("invalid backend response envelope"))
	}
	if e = json.Unmarshal(envelope.Data, out); e != nil {
		return requestFailure(method, key, fmt.Errorf("invalid backend response data: %w", e))
	}
	return nil
}
func (c *Client) Check(ctx context.Context) error {
	var health struct {
		Status  string `json:"status"`
		Version string `json:"api_version"`
	}
	// healthz is public, but the same client preserves URL/redirect policy.
	if e := c.request(ctx, "GET", "/healthz", "", "", nil, &health); e != nil {
		return e
	}
	if health.Status != "ok" || health.Version != "v1" {
		return fmt.Errorf("unsupported backend health/API version")
	}
	var caps struct {
		Version      string   `json:"api_version"`
		Capabilities []string `json:"capabilities"`
	}
	if e := c.request(ctx, "GET", "/v1/capabilities", "", "", nil, &caps); e != nil {
		return e
	}
	if caps.Version != "v1" {
		return fmt.Errorf("unsupported backend API version")
	}
	for _, required := range []string{"projects", "sessions", "idempotent_init"} {
		found := false
		for _, cap := range caps.Capabilities {
			if cap == required {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("backend does not support %s", required)
		}
	}
	return nil
}
func (c *Client) CreateProject(ctx context.Context, key, name string) (Project, error) {
	var out struct {
		Project Project `json:"project"`
	}
	e := c.request(ctx, "POST", "/v1/projects", "", key, struct {
		Name string `json:"name"`
	}{name}, &out)
	if e == nil && !config.ValidBackendProjectID(out.Project.ID) {
		e = fmt.Errorf("backend returned invalid project ID")
	}
	return out.Project, e
}
func (c *Client) RegisterInitSession(ctx context.Context, project, key string) (string, error) {
	var out struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	e := c.request(ctx, "POST", "/v1/projects/"+project+"/sessions", "", key, struct {
		Name      string `json:"name"`
		ContextID string `json:"context_id"`
		AgentType string `json:"agent_type"`
	}{"td init", "backend-init:" + key, "cli"}, &out)
	if e == nil && (!strings.HasPrefix(out.Session.ID, "ses_") || !config.ValidBackendProjectID("prj_"+strings.TrimPrefix(out.Session.ID, "ses_"))) {
		e = fmt.Errorf("backend returned invalid session ID")
	}
	return out.Session.ID, e
}
func (c *Client) GetProject(ctx context.Context, id, session string) (Project, error) {
	var out struct {
		Project Project `json:"project"`
	}
	e := c.request(ctx, "GET", "/v1/projects/"+id, session, "", nil, &out)
	if e == nil && (!config.ValidBackendProjectID(out.Project.ID) || out.Project.ID != id) {
		e = fmt.Errorf("backend returned a different or invalid project ID")
	}
	return out.Project, e
}
