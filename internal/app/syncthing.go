package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/syncthing"
)

type SyncthingClient interface {
	CanonicalID(context.Context, string) (string, error)
	LocalID(context.Context) (string, error)
	ListDevices(context.Context) ([]syncthing.Device, error)
	ConfirmPrivacy(context.Context) error
	BackupFolder(context.Context, string) (syncthing.BackupFolder, error)
	AddDevice(context.Context, string) error
	ShareBackup(context.Context, syncthing.BackupFolder, string) error
	RemoveDevice(context.Context, string) error
}

type SyncthingOutcome int

const (
	SyncthingNotChanged SyncthingOutcome = iota
	SyncthingComplete
	SyncthingPartial
	SyncthingUnknown
)

type SyncthingResult struct {
	Outcome  SyncthingOutcome
	DeviceID string
	LocalID  string
	Err      error
}

// Syncthing owns bounded runtime workflows; the daemon remains configuration
// authority. A successful pair configures this node, not the remote receiver.
type Syncthing struct {
	client func() (SyncthingClient, error)
	lock   func(func() error) error
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	calls  sync.WaitGroup
}

func NewSyncthing() *Syncthing {
	ctx, cancel := context.WithCancel(context.Background())
	return &Syncthing{ctx: ctx, cancel: cancel, lock: syncthing.WithMutationLock, client: func() (SyncthingClient, error) {
		key, err := helper.ReadBoardString(paths.StateSyncthingAPIKey)
		if err != nil {
			return nil, errors.New("Syncthing credentials unavailable; maintenance inspection is required")
		}
		return syncthing.NewClient(key), nil
	}}
}
func (s *Syncthing) begin() (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("Syncthing workflow is shutting down")
	}
	s.calls.Add(1)
	ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
	return ctx, func() { cancel(); s.calls.Done() }, nil
}

// Close cancels and joins local calls. Accepted daemon writes are not rolled back.
func (s *Syncthing) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.calls.Wait()
}
func (s *Syncthing) ListDevices() ([]syncthing.Device, error) {
	ctx, done, err := s.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := s.client()
	if err != nil {
		return nil, err
	}
	return c.ListDevices(ctx)
}
func (s *Syncthing) Pair(input string) SyncthingResult {
	result := SyncthingResult{}
	ctx, done, err := s.begin()
	if err != nil {
		result.Err = err
		return result
	}
	defer done()
	result.Err = s.lock(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := s.client()
		if err != nil {
			return err
		}
		id, err := c.CanonicalID(ctx, input)
		if err != nil {
			return err
		}
		result.DeviceID = id
		local, err := c.LocalID(ctx)
		if err != nil {
			return err
		}
		result.LocalID = local
		if id == local {
			return errors.New("cannot pair this node with itself")
		}
		if err = c.ConfirmPrivacy(ctx); err != nil {
			return err
		}
		folder, err := c.BackupFolder(ctx, local)
		if err != nil {
			return err
		}
		devices, err := c.ListDevices(ctx)
		if err != nil {
			return err
		}
		exists := false
		for _, d := range devices {
			if d.DeviceID == id {
				exists = true
				break
			}
		}
		if exists && folder.HasDevice(id) {
			return errors.New("device already paired and sharing the backup folder")
		}
		if !exists {
			// Once a write is attempted, an error cannot establish that nothing changed.
			result.Outcome = SyncthingUnknown
			if err = c.AddDevice(ctx, id); err != nil {
				return fmt.Errorf("device addition was not confirmed; inspect current devices before retrying: %w", err)
			}
		}
		result.Outcome = SyncthingPartial
		// Re-read under the VPN lock; never reuse a screen's older folder snapshot.
		folder, err = c.BackupFolder(ctx, local)
		if err != nil {
			return fmt.Errorf("device is configured, but backup sharing is incomplete: %w", err)
		}
		result.Outcome = SyncthingUnknown
		if err = c.ShareBackup(ctx, folder, id); err != nil {
			return fmt.Errorf("device is configured; backup sharing was not confirmed: %w", err)
		}
		devices, err = c.ListDevices(ctx)
		if err != nil {
			return fmt.Errorf("pairing was accepted, but its current state could not be confirmed: %w", err)
		}
		for _, device := range devices {
			if device.DeviceID == id && device.BackupKnown && device.BackupShared {
				result.Outcome = SyncthingComplete
				return nil
			}
		}
		return errors.New("backup sharing is not confirmed in the current configuration; inspect before retrying")
	})
	return result
}

func (s *Syncthing) Remove(input string) SyncthingResult {
	result := SyncthingResult{}
	ctx, done, err := s.begin()
	if err != nil {
		result.Err = err
		return result
	}
	defer done()
	result.Err = s.lock(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := s.client()
		if err != nil {
			return err
		}
		id, err := c.CanonicalID(ctx, input)
		if err != nil {
			return err
		}
		result.DeviceID = id
		local, err := c.LocalID(ctx)
		if err != nil {
			return err
		}
		result.LocalID = local
		if id == local {
			return errors.New("cannot remove this node's own Syncthing identity")
		}
		devices, err := c.ListDevices(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, d := range devices {
			if d.DeviceID == id {
				found = true
				break
			}
		}
		if !found {
			return errors.New("device is no longer configured; refresh the device list")
		}
		result.Outcome = SyncthingUnknown
		if err = c.RemoveDevice(ctx, id); err != nil {
			return fmt.Errorf("device removal was not confirmed; inspect the current device list before retrying: %w", err)
		}
		devices, err = c.ListDevices(ctx)
		if err != nil {
			return fmt.Errorf("removal was accepted, but its current state could not be confirmed: %w", err)
		}
		for _, device := range devices {
			if device.DeviceID == id {
				return errors.New("device is still configured; removal is not confirmed")
			}
		}
		folder, err := c.BackupFolder(ctx, local)
		if err != nil {
			return fmt.Errorf("device is absent, but backup share removal could not be confirmed: %w", err)
		}
		if folder.HasDevice(id) {
			return errors.New("device is absent, but its backup share remains; removal is not confirmed")
		}
		result.Outcome = SyncthingComplete
		return nil
	})
	return result
}
