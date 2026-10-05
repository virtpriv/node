// Package syncthing owns loopback access to the pinned Syncthing daemon.
package syncthing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/paths"
	"golang.org/x/sys/unix"
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
			hint = "; API key rejected, administrator inspection is required"
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

// CanonicalID delegates checksum and protocol parsing to the pinned daemon.
func (c *Client) CanonicalID(ctx context.Context, input string) (string, error) {
	if strings.TrimSpace(input) == "" || len(input) > 128 {
		return "", errors.New("enter a Syncthing device ID")
	}
	var result struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	if err := c.read(ctx, "/rest/svc/deviceid?id="+url.QueryEscape(input), &result); err != nil {
		return "", err
	}
	if result.ID == "" || result.Error != "" {
		return "", errors.New("invalid Syncthing device ID")
	}
	return result.ID, nil
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

type Device struct {
	Name         string
	DeviceID     string
	BackupKnown  bool
	BackupShared bool
}

func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	local, err := c.LocalID(ctx)
	if err != nil {
		return nil, err
	}
	// Read devices and folder membership together. Never infer backup sharing
	// merely from the existence of a remote device.
	var cfg struct {
		Devices json.RawMessage `json:"devices"`
		Folders []BackupFolder  `json:"folders"`
	}
	if err := c.read(ctx, "/rest/config", &cfg); err != nil {
		return nil, err
	}
	devices, err := parseDevices(cfg.Devices, local)
	if err != nil {
		return nil, err
	}
	known := cfg.Folders != nil
	var backup BackupFolder
	for _, folder := range cfg.Folders {
		if folder.ID == "lnd-backup" {
			backup = folder
			known = folder.validate(local) == nil
			break
		}
	}
	for i := range devices {
		devices[i].BackupKnown = known
		devices[i].BackupShared = known && backup.HasDevice(devices[i].DeviceID)
	}
	return devices, nil
}

func parseDevices(
	raw []byte, localID string,
) ([]Device, error) {
	seen := make(map[string]bool)
	var entries []struct {
		DeviceID string `json:"deviceID"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode Syncthing devices: %w", err)
	}
	if entries == nil {
		return nil, errors.New("syncthing device configuration unavailable")
	}
	devices := make([]Device, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.DeviceID)
		if id == "" || id == localID || seen[id] {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = "Syncthing device"
		}
		devices = append(devices, Device{Name: name, DeviceID: id})
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Name == devices[j].Name {
			return devices[i].DeviceID < devices[j].DeviceID
		}
		return devices[i].Name < devices[j].Name
	})
	return devices, nil
}

// WithMutationLock coordinates VPN processes using the stable root-owned board
// directory. No credential file is locked or written. Contention refuses promptly.
// External Web UI/API writers do not participate in this advisory lock.
func WithMutationLock(action func() error) error { return withMutationLock(paths.StateDir, action) }
func withMutationLock(dir string, action func() error) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open Syncthing operation lock: %w", err)
	}
	defer f.Close()
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("another VPN Syncthing operation is active or the lock is unavailable; retry after checking its result")
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return action()
}
