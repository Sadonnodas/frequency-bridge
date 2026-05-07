package spectera

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a thin HTTPS client for the SSCv2 endpoints we consume.
type Client struct {
	httpClient *http.Client
	baseURL    string
	username   string
	password   string
}

func newClient(cfg Config) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}, //nolint:gosec // user-controlled per device, off by default
	}
	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			// SSE streams must not be cut off; per-request timeouts go via ctx.
			Timeout: 0,
		},
		baseURL:  strings.TrimRight(cfg.BaseURL, "/"),
		username: cfg.Username,
		password: cfg.Password,
	}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// ListInputs fetches /api/audio/inputs.
func (c *Client) ListInputs(ctx context.Context) ([]Input, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := c.newRequest(reqCtx, http.MethodGet, "/api/audio/inputs", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list inputs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list inputs: %s", resp.Status)
	}
	var out []Input
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode inputs: %w", err)
	}
	return out, nil
}

// PutInput PUTs a partial-update body to /api/audio/inputs/{id}. fields is a
// JSON object containing only the keys to change (e.g. {"frequency":580125}
// or {"mute":true}). The server applies the patch atomically and emits an
// SSE notification for the resource.
func (c *Client) PutInput(ctx context.Context, id int, fields map[string]any) error {
	body, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := c.newRequest(reqCtx, http.MethodPut, fmt.Sprintf("/api/audio/inputs/%d", id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("put input %d: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return errInputNotFound
	}
	if resp.StatusCode == http.StatusBadRequest {
		return fmt.Errorf("put input %d: %s", id, resp.Status)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("put input %d: %s", id, resp.Status)
	}
	return nil
}

var errInputNotFound = errors.New("input not found")

// SetSubscriptionPaths PUTs the full set of paths for a subscription. Per
// SSCv2 spec, the server responds with the initial values of any newly-added
// paths over the SSE stream.
func (c *Client) SetSubscriptionPaths(ctx context.Context, sessionUUID string, paths []string) error {
	body, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := c.newRequest(reqCtx, http.MethodPut, "/api/ssc/state/subscriptions/"+sessionUUID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("set subscription paths: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("set subscription paths: %s", resp.Status)
	}
	return nil
}

// SSEEvent is one parsed event from an SSE stream.
type SSEEvent struct {
	Type string
	Data []byte
}

// Subscription represents an open /api/ssc/state/subscriptions stream.
type Subscription struct {
	// SessionURL is the value of the Content-Location response header, e.g.
	// "/api/ssc/state/subscriptions/<uuid>".
	SessionURL string
	// SessionUUID is extracted from the open event's payload. Closed-via-
	// channel: wait on Ready before reading SessionUUID.
	SessionUUID string

	// Ready is closed once the open event has been parsed (i.e. SessionUUID
	// is filled in). If the stream errors before then, Ready is closed
	// without setting SessionUUID — check len(SessionUUID) > 0.
	Ready chan struct{}

	body   io.ReadCloser
	events chan SSEEvent
}

// Events returns the channel of resource-update messages. The channel is
// closed when the stream ends (server close, network error, or ctx cancel).
func (s *Subscription) Events() <-chan SSEEvent { return s.events }

// Close terminates the stream. Idempotent.
func (s *Subscription) Close() error {
	if s.body != nil {
		err := s.body.Close()
		s.body = nil
		return err
	}
	return nil
}

// Subscribe opens an SSE state-subscription stream. After opening, the
// subscription is empty — call SetSubscriptionPaths to register interest in
// specific resources.
func (c *Client) Subscribe(ctx context.Context) (*Subscription, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/ssc/state/subscriptions", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("subscribe: %s", resp.Status)
	}
	sub := &Subscription{
		SessionURL: resp.Header.Get("Content-Location"),
		Ready:      make(chan struct{}),
		body:       resp.Body,
		events:     make(chan SSEEvent, 32),
	}
	go sub.parse(ctx)
	return sub, nil
}

func (s *Subscription) parse(ctx context.Context) {
	defer close(s.events)
	scanner := bufio.NewScanner(s.body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var ev SSEEvent
	readyClosed := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if ev.Type != "" || len(ev.Data) > 0 {
				if ev.Type == "" {
					ev.Type = "message"
				}
				if !readyClosed && ev.Type == "open" {
					var openPayload struct {
						SessionUUID string `json:"sessionUUID"`
					}
					if err := json.Unmarshal(ev.Data, &openPayload); err == nil {
						s.SessionUUID = openPayload.SessionUUID
					}
					close(s.Ready)
					readyClosed = true
				}
				select {
				case s.events <- ev:
				case <-ctx.Done():
					if !readyClosed {
						close(s.Ready)
					}
					return
				}
			}
			ev = SSEEvent{}
			continue
		}
		if v, ok := stripPrefix(line, "event: "); ok {
			ev.Type = v
		} else if v, ok := stripPrefix(line, "data: "); ok {
			if len(ev.Data) > 0 {
				ev.Data = append(ev.Data, '\n')
			}
			ev.Data = append(ev.Data, v...)
		}
	}
	if !readyClosed {
		close(s.Ready)
	}
}

func stripPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return strings.TrimPrefix(s, prefix), true
	}
	return "", false
}
