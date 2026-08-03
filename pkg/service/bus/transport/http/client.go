package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"

	"github.com/54c1/niq/core/event"
)

// HttpClient implements EventBusClient over HTTP. It is the network
// counterpart of InProcessClient — same interface, same capabilities,
// different transport path. For loopback connections no token is needed;
// for remote connections a token authenticates the worker identity.
type HttpClient struct {
	workerID string
	baseURL  string
	token    string // empty ⇒ loopback, workerID self-declared in body
	client   *stdhttp.Client
}

// NewHttpClient creates an HTTP-backed EventBusClient.
// token may be empty for loopback connections (self-declared workerID).
func NewHttpClient(baseURL, workerID, token string) *HttpClient {
	return &HttpClient{
		workerID: workerID,
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
		client:   &stdhttp.Client{},
	}
}

// ── EventBusClient ──

func (c *HttpClient) Subscribe(patterns []event.EventPattern) error {
	return c.post("/subscribe", map[string]any{
		"worker_id": c.workerID,
		"patterns":  patterns,
	})
}

func (c *HttpClient) Unsubscribe(patterns []event.EventPattern) error {
	return c.post("/unsubscribe", map[string]any{
		"worker_id": c.workerID,
		"patterns":  patterns,
	})
}

func (c *HttpClient) Publish(events ...event.Event) error {
	return c.post("/publish", map[string]any{
		"worker_id": c.workerID,
		"events":    events,
	})
}

// Receive connects to the SSE stream and bridges events to a Go channel.
// The returned channel receives events that match this worker's subscriptions.
// When ctx is cancelled the SSE connection is closed and the channel drained.
func (c *HttpClient) Receive(ctx context.Context) (chan event.Event, error) {
	url := fmt.Sprintf("%s/events?worker_id=%s", c.baseURL, c.workerID)
	req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpclient: SSE connect: %w", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("httpclient: SSE connect failed (%d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan event.Event, 64)
	go c.readSSE(ctx, resp.Body, ch)
	return ch, nil
}

// readSSE reads Server-Sent Events from r and forwards them to ch.
// It stops when ctx is cancelled or the body stream ends.
func (c *HttpClient) readSSE(ctx context.Context, r io.ReadCloser, ch chan event.Event) {
	defer r.Close()
	defer close(ch)

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := r.Read(tmp)
		if err != nil {
			return
		}
		buf = append(buf, tmp[:n]...)

		// Extract complete SSE messages ("data: ...\n\n").
		for {
			idx := bytes.Index(buf, []byte("\n\n"))
			if idx < 0 {
				break
			}
			line := string(buf[:idx])
			buf = buf[idx+2:]

			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			var evt event.Event
			if err := json.Unmarshal([]byte(payload), &evt); err != nil {
				continue
			}
			select {
			case ch <- evt:
			case <-ctx.Done():
				return
			}
		}
	}
}

// ── Helpers ──

func (c *HttpClient) post(path string, body any) error {
	data, _ := json.Marshal(body)
	req, err := stdhttp.NewRequest(stdhttp.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("httpclient: %s %s: %w", req.Method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK && resp.StatusCode != stdhttp.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("httpclient: %s %s returned %d: %s", req.Method, path, resp.StatusCode, string(respBody))
	}
	return nil
}
