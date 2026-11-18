// Package clickup is a small client for the parts of the ClickUp v2 API clctl uses.
package clickup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/JB0t/clctl/internal/tracker"
)

// IsCustomTaskID reports whether id looks like a custom task ID (TECH-1234) rather than a
// raw one.
func IsCustomTaskID(id string) bool { return tracker.IsTaskKey(id) }

// APIError is a non-2xx response from ClickUp.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("ClickUp API returned HTTP %d", e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("ClickUp API: %s (%s, HTTP %d)", e.Message, e.Code, e.Status)
	}
	return fmt.Sprintf("ClickUp API: %s (HTTP %d)", e.Message, e.Status)
}

// IsNotFound reports whether err is a ClickUp 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

type Client struct {
	Token   string
	BaseURL string
	TeamID  string
	HTTP    *http.Client
	// MaxRetries bounds retries on HTTP 429 (rate limited).
	MaxRetries int
}

// Client is a ClickUp tracker.Backend.
var _ tracker.Backend = (*Client)(nil)

func New(token, baseURL, teamID string) *Client {
	return &Client{
		Token:      token,
		BaseURL:    baseURL,
		TeamID:     teamID,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		MaxRetries: 3,
	}
}

// Kind is "clickup".
func (c *Client) Kind() string { return "clickup" }

// WorkspaceID is the ClickUp workspace (team) ID.
func (c *Client) WorkspaceID() string { return c.TeamID }

// TaskQuery returns query params that let the API accept a custom task ID.
func (c *Client) TaskQuery(taskID string) url.Values {
	query := url.Values{}
	if IsCustomTaskID(taskID) {
		query.Set("custom_task_ids", "true")
		query.Set("team_id", c.TeamID)
	}
	return query
}

// Do sends a JSON request and decodes the JSON response into out (which may be nil).
func (c *Client) Do(method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	return c.send(method, path, query, "application/json", func() io.Reader { return bytes.NewReader(payload) }, body != nil, out)
}

// Upload sends a multipart request built by the caller.
func (c *Client) Upload(path string, query url.Values, contentType string, body []byte, out any) error {
	return c.send(http.MethodPost, path, query, contentType, func() io.Reader { return bytes.NewReader(body) }, true, out)
}

func (c *Client) send(method, path string, query url.Values, contentType string, body func() io.Reader, hasBody bool, out any) error {
	target := c.BaseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if hasBody {
			reader = body()
		}
		request, err := http.NewRequest(method, target, reader)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", c.Token)
		if hasBody {
			request.Header.Set("Content-Type", contentType)
		}
		response, err := c.HTTP.Do(request)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return err
		}
		if response.StatusCode == http.StatusTooManyRequests && attempt < c.MaxRetries {
			time.Sleep(retryDelay(response.Header))
			continue
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			apiErr := &APIError{Status: response.StatusCode}
			var detail struct {
				Err   string `json:"err"`
				Ecode string `json:"ECODE"`
			}
			if json.Unmarshal(data, &detail) == nil {
				apiErr.Message, apiErr.Code = detail.Err, detail.Ecode
			}
			return apiErr
		}
		if out == nil || len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		return json.Unmarshal(data, out)
	}
}

// retryDelay waits until ClickUp's rate-limit window resets (bounded to 1-60s).
func retryDelay(header http.Header) time.Duration {
	if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		wait := time.Until(time.Unix(reset, 0))
		if wait > time.Minute {
			wait = time.Minute
		}
		if wait > time.Second {
			return wait
		}
	}
	return 2 * time.Second
}
