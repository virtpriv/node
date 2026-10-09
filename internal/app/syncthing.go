package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/syncthing"
	"github.com/virtpriv/node/internal/system"
)

type SyncthingClient interface {
	CanonicalID(context.Context, string) (string, error)
	LocalID(context.Context) (string, error)
	ListDevices(context.Context) ([]syncthing.Device, error)
	BackupDelivery(context.Context, []syncthing.Device) (map[string]syncthing.Delivery, error)
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

// Syncthing owns bounded runtime workflows. The daemon remains configuration
// authority. A successful pair configures this node, not the remote receiver.
type Syncthing struct {
	client     func() (SyncthingClient, error)
	backupCopy func(context.Context) (BackupCopy, error)
	lock       func(func() error) error
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	calls      sync.WaitGroup
}

func NewSyncthing() *Syncthing {
	ctx, cancel := context.WithCancel(context.Background())
	return &Syncthing{ctx: ctx, cancel: cancel, lock: syncthing.WithMutationLock, backupCopy: readBackupCopy, client: func() (SyncthingClient, error) {
		key, err := helper.ReadBoardString(paths.StateSyncthingAPIKey)
		if err != nil {
			return nil, errors.New("Syncthing credentials unavailable, maintenance inspection is required")
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
				return fmt.Errorf("device addition was not confirmed, inspect current devices before retrying: %w", err)
			}
		}
		result.Outcome = SyncthingPartial
		// Re-read under the VPN lock. Never reuse a screen's older folder snapshot.
		folder, err = c.BackupFolder(ctx, local)
		if err != nil {
			return fmt.Errorf("device is configured, but backup sharing is incomplete: %w", err)
		}
		result.Outcome = SyncthingUnknown
		if err = c.ShareBackup(ctx, folder, id); err != nil {
			return fmt.Errorf("device is configured, backup sharing was not confirmed: %w", err)
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
		return errors.New("backup sharing is not confirmed in the current configuration, inspect before retrying")
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
			return errors.New("device is no longer configured, refresh the device list")
		}
		result.Outcome = SyncthingUnknown
		if err = c.RemoveDevice(ctx, id); err != nil {
			return fmt.Errorf("device removal was not confirmed, inspect the current device list before retrying: %w", err)
		}
		devices, err = c.ListDevices(ctx)
		if err != nil {
			return fmt.Errorf("removal was accepted, but its current state could not be confirmed: %w", err)
		}
		for _, device := range devices {
			if device.DeviceID == id {
				return errors.New("device is still configured, removal is not confirmed")
			}
		}
		folder, err := c.BackupFolder(ctx, local)
		if err != nil {
			return fmt.Errorf("device is absent, but backup share removal could not be confirmed: %w", err)
		}
		if folder.HasDevice(id) {
			return errors.New("device is absent, but its backup share remains. Removal is not confirmed")
		}
		result.Outcome = SyncthingComplete
		return nil
	})
	return result
}

// SyncthingObservation is one read for the Syncthing screens. The backup copy
// comes from systemd and stays readable when Syncthing is not.
type SyncthingObservation struct {
	Devices     []syncthing.Device
	DevicesErr  error
	Delivery    map[string]syncthing.Delivery
	DeliveryErr error
	Copy        BackupCopy
	CopyErr     error
}

// Observe reads the copy job's record, the devices and what each device holds.
func (s *Syncthing) Observe() SyncthingObservation {
	var o SyncthingObservation
	ctx, done, err := s.begin()
	if err != nil {
		o.DevicesErr, o.DeliveryErr, o.CopyErr = err, err, err
		return o
	}
	defer done()
	o.Copy, o.CopyErr = s.backupCopy(ctx)
	c, err := s.client()
	if err != nil {
		o.DevicesErr, o.DeliveryErr = err, err
		return o
	}
	if o.Devices, o.DevicesErr = c.ListDevices(ctx); o.DevicesErr != nil {
		o.DeliveryErr = o.DevicesErr
		return o
	}
	o.Delivery, o.DeliveryErr = c.BackupDelivery(ctx, o.Devices)
	return o
}

// BackupCopyState is what systemd's record says about the node's own copy of
// LND's channel backup, the copy Syncthing sends.
type BackupCopyState int

const (
	BackupCopyCurrent BackupCopyState = iota
	BackupCopyFailed
	BackupCopyLate
	BackupCopyChecking
	BackupCopyNotYet
	BackupCopyOff
	BackupCopyNoTimer
	// BackupCopyStuck: a run started more than the limit ago and has not
	// finished. When the last finished run was is not known.
	BackupCopyStuck
)

// BackupCopy holds the state and the time that goes with it: the end of the
// last run, the start of a run in progress, or the time the timer was switched
// on while no run has finished since. The time is zero when systemd has none.
type BackupCopy struct {
	State BackupCopyState
	At    time.Time
}

// backupCopyLateAfter allows two missed minutes. The timer starts the copy job
// every minute, and every run compares the copy with LND's backup.
const backupCopyLateAfter = 3 * time.Minute

// Judged applies the time limit at now, so a record read a while ago never
// keeps saying the copy is current. A run that hangs is stuck, and a timer
// switched on long ago without a finished run counts as late.
func (c BackupCopy) Judged(now time.Time) BackupCopy {
	late := !c.At.IsZero() && now.Sub(c.At) > backupCopyLateAfter
	switch {
	case !late:
		return c
	case c.State == BackupCopyCurrent:
		return BackupCopy{State: BackupCopyLate, At: c.At}
	case c.State == BackupCopyChecking:
		return BackupCopy{State: BackupCopyStuck, At: c.At}
	case c.State == BackupCopyNotYet:
		return BackupCopy{State: BackupCopyLate}
	}
	return c
}

var (
	backupTimerUnit  = filepath.Base(paths.BackupCheckTimer)
	backupExportUnit = filepath.Base(paths.BackupExportService)
)

func readBackupCopy(ctx context.Context) (BackupCopy, error) {
	units, err := system.ReadUnitProperties(ctx, []string{backupTimerUnit, backupExportUnit},
		"LoadState", "ActiveState", "Result", "ActiveEnterTimestamp",
		"ExecMainStartTimestamp", "ExecMainExitTimestamp", "InactiveEnterTimestamp")
	if err != nil {
		return BackupCopy{}, fmt.Errorf("read the backup copy record: %w", err)
	}
	return backupCopyFrom(units)
}

func parseBackupCopy(out string) (BackupCopy, error) {
	return backupCopyFrom(system.ParseUnitProperties(out))
}

// backupCopyFrom never reports a current copy from an incomplete record.
func backupCopyFrom(units map[string]map[string]string) (BackupCopy, error) {
	timer, export := units[backupTimerUnit], units[backupExportUnit]
	if timer == nil || export == nil {
		return BackupCopy{}, errors.New("backup copy record incomplete")
	}
	switch {
	case timer["LoadState"] == "not-found":
		return BackupCopy{State: BackupCopyNoTimer}, nil
	case timer["LoadState"] != "loaded" || timer["ActiveState"] != "active":
		return BackupCopy{State: BackupCopyOff}, nil
	case export["LoadState"] != "loaded":
		return BackupCopy{}, errors.New("backup copy job not loaded")
	}
	stamp := func(unit map[string]string, key string) (time.Time, error) {
		value := unit[key]
		if value == "" {
			return time.Time{}, nil
		}
		seconds, err := strconv.ParseInt(strings.TrimPrefix(value, "@"), 10, 64)
		if err != nil || !strings.HasPrefix(value, "@") {
			return time.Time{}, fmt.Errorf("backup copy time %s=%q not understood", key, value)
		}
		return time.Unix(seconds, 0), nil
	}
	// A starting run has its own time. systemd clears the result at each
	// start, so the result below belongs to the last finished run.
	if export["ActiveState"] == "activating" {
		started, err := stamp(export, "ExecMainStartTimestamp")
		return BackupCopy{State: BackupCopyChecking, At: started}, err
	}
	exited, err := stamp(export, "ExecMainExitTimestamp")
	if err != nil {
		return BackupCopy{}, err
	}
	if export["Result"] != "success" {
		// A failure before the program ran leaves the exit time of an older
		// run, so the time the job failed comes first.
		failed, err := stamp(export, "InactiveEnterTimestamp")
		if failed.IsZero() {
			failed = exited
		}
		return BackupCopy{State: BackupCopyFailed, At: failed}, err
	}
	if exited.IsZero() {
		// LastTriggerUSec would be closer, but systemctl prints it as a date
		// even with --timestamp=unix. The timer's start time is a timestamp.
		since, err := stamp(timer, "ActiveEnterTimestamp")
		return BackupCopy{State: BackupCopyNotYet, At: since}, err
	}
	return BackupCopy{State: BackupCopyCurrent, At: exited}, nil
}
