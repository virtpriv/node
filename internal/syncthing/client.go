// Package syncthing owns loopback access to the pinned Syncthing daemon.
package syncthing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const GUIBase = "http://127.0.0.1:8384"
const responseLimit = 10 << 20

// Client is immutable after construction and may serve concurrent reads.
// Credentials are supplied by the caller's existing privilege boundary.
type Client struct {
	base string
	key  string
	http *http.Client
}

func NewClient(key string) *Client { return newClient(GUIBase, key) }
func newClient(base, key string) *Client {
	return &Client{base: base, key: key, http: &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Request never follows redirects or consults proxy environment variables.
// Error bodies are discarded because they may contain configuration secrets.
func (c *Client) Request(ctx context.Context, method, endpoint, body string) (string, error) {
	route, _, _ := strings.Cut(endpoint, "?")
	fail := func(err error) (string, error) { return "", fmt.Errorf("syncthing API %s %s: %w", method, route, err) }
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+endpoint, reader)
	if err != nil {
		return fail(err)
	}
	if c.key != "" {
		req.Header.Set("X-API-Key", c.key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fail(errors.New("local daemon request failed or timed out"))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		hint := ""
		if resp.StatusCode == http.StatusForbidden {
			hint = ", API key rejected, administrator inspection is required"
		}
		return fail(fmt.Errorf("HTTP %d%s", resp.StatusCode, hint))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil {
		return fail(errors.New("response could not be read"))
	}
	if len(data) > responseLimit {
		return fail(errors.New("response exceeds size limit"))
	}
	return string(data), nil
}

func (c *Client) read(ctx context.Context, endpoint string, result any) error {
	raw, err := c.Request(ctx, http.MethodGet, endpoint, "")
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(raw), result); err != nil {
		return errors.New("invalid Syncthing response")
	}
	return nil
}

func (c *Client) LocalID(ctx context.Context) (string, error) {
	var result struct {
		ID string `json:"myID"`
	}
	if err := c.read(ctx, "/rest/system/status", &result); err != nil {
		return "", err
	}
	if result.ID == "" {
		return "", errors.New("local Syncthing identity unavailable")
	}
	return result.ID, nil
}
