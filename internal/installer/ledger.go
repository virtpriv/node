// internal/installer/ledger.go

package installer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/config"
)

// The ledger is root-private historical authority for exactly one bounded
// base-install lifecycle. It records immutable resume context, completed base
// steps, and a terminal status. It never records add-ons, later settings,
// upgrades, or present-tense service health.
const ledgerSchema = 1

const (
	ledgerInProgress = "in_progress"
	ledgerComplete   = "complete"
)

var baseInstallStepKeys = []string{
	"binary.install",
	"apt.base",
	"firewall",
	"host.prep",
	"identity.access",
	"service-identities.v1",
	"ipv6.disable",
	"tor.configure",
	"tor.gate",
	"apt.torproxy",
	"base.upgrade",
	"btc.download",
	"btc.verify",
	"btc.install",
	"btc.start",
	"security",
	"lnd.download",
	"lnd.verify",
	"lnd.install",
	"tor.lnd",
	"lnd.configure",
	"lnd.start",
	"lnd.tls-san",
	"lnd.certwatch",
	"ssh.harden",
	"journal.access",
	"helper.enable",
	"state.stage",
	"shellenv",
}

var bakeInstallStepKeys = []string{
	"binary.install",
	"apt.base",
	"firewall",
	"host.prep",
	"service-identities.v1",
	"ipv6.disable",
	"tor.configure",
	"tor.gate",
	"apt.torproxy",
	"base.upgrade",
	"btc.download",
	"btc.verify",
	"btc.install",
	"btc.start",
	"security",
	"lnd.download",
	"lnd.verify",
	"lnd.install",
	"tor.lnd",
	"lnd.configure",
	"lnd.start",
	"lnd.tls-san",
	"lnd.certwatch",
	"shellenv",
}

type ledgerEntry struct {
	CompletedAt string `json:"completed_at"`
	Version     string `json:"version"`
}

type installContext struct {
	Network        string `json:"network"`
	InitialP2PMode string `json:"initial_p2p_mode"`
	DbCacheMB      *int   `json:"db_cache_mb,omitempty"`
}

type installLedger struct {
	Schema  int                    `json:"schema"`
	Status  string                 `json:"status"`
	Context installContext         `json:"context"`
	Steps   map[string]ledgerEntry `json:"steps"`
}

func newLedger(ctx installContext) (*installLedger, error) {
	l := &installLedger{
		Schema:  ledgerSchema,
		Status:  ledgerInProgress,
		Context: ctx,
		Steps:   map[string]ledgerEntry{},
	}
	if err := validateLedger(l); err != nil {
		return nil, err
	}
	return l, nil
}

func parseLedger(data []byte) (*installLedger, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var l installLedger
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("decode ledger: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return nil, err
	}
	if err := validateLedger(&l); err != nil {
		return nil, err
	}
	return &l, nil
}

func requireJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("ledger contains trailing JSON value")
	}
	return fmt.Errorf("ledger trailing data: %w", err)
}

func validateLedger(l *installLedger) error {
	if l.Schema != ledgerSchema {
		if l.Schema > ledgerSchema {
			return fmt.Errorf("ledger schema %d is newer than supported %d",
				l.Schema, ledgerSchema)
		}
		return fmt.Errorf("ledger schema is %d, want %d", l.Schema, ledgerSchema)
	}
	if l.Status != ledgerInProgress && l.Status != ledgerComplete {
		return fmt.Errorf("invalid ledger status %q", l.Status)
	}
	if err := config.ValidateNetwork(l.Context.Network); err != nil {
		return fmt.Errorf("invalid install network %q", l.Context.Network)
	}
	if l.Context.InitialP2PMode != "tor" &&
		l.Context.InitialP2PMode != "hybrid" {
		return fmt.Errorf("invalid initial P2P mode %q",
			l.Context.InitialP2PMode)
	}
	if l.Context.DbCacheMB != nil && !validDbCache(*l.Context.DbCacheMB) {
		return fmt.Errorf("invalid db cache %d", *l.Context.DbCacheMB)
	}
	if l.Steps == nil {
		return fmt.Errorf("ledger steps must be an object")
	}
	allowed := baseStepKeySet()
	for key, entry := range l.Steps {
		if !allowed[key] {
			return fmt.Errorf("unknown base-install step %q", key)
		}
		if strings.TrimSpace(entry.Version) == "" {
			return fmt.Errorf("step %q has empty version", key)
		}
		if _, err := time.Parse(time.RFC3339, entry.CompletedAt); err != nil {
			return fmt.Errorf("step %q has invalid completed_at: %w", key, err)
		}
	}
	if len(l.Steps) > 0 && l.Context.DbCacheMB == nil {
		return fmt.Errorf("ledger records install steps without a db cache decision")
	}
	if !validInstallProgressSubset(l.Steps) {
		return fmt.Errorf("ledger step set is not a reachable base-install state")
	}
	if l.Status == ledgerComplete && !l.allBaseStepsDone() {
		return fmt.Errorf("complete ledger is missing base-install steps")
	}
	return nil
}

// A process can add only a prefix of the full run or a prefix of the bake run.
// Across resumes the durable set is therefore the union of one prefix from
// each supported sequence. Other subsets were not produced by this installer
// and are ambiguous rather than automatic repair instructions.
func validInstallProgressSubset(steps map[string]ledgerEntry) bool {
	want := make(map[string]bool, len(steps))
	for key := range steps {
		want[key] = true
	}
	for fullN := 0; fullN <= len(baseInstallStepKeys); fullN++ {
		for bakeN := 0; bakeN <= len(bakeInstallStepKeys); bakeN++ {
			candidate := map[string]bool{}
			for _, key := range baseInstallStepKeys[:fullN] {
				candidate[key] = true
			}
			for _, key := range bakeInstallStepKeys[:bakeN] {
				candidate[key] = true
			}
			if sameStepSet(candidate, want) {
				return true
			}
		}
	}
	return false
}

func sameStepSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if !b[key] {
			return false
		}
	}
	return true
}

func validDbCache(v int) bool {
	return v == 512 || v == 1024 || v == 2048
}

func baseStepKeySet() map[string]bool {
	set := make(map[string]bool, len(baseInstallStepKeys))
	for _, key := range baseInstallStepKeys {
		set[key] = true
	}
	return set
}

func (l *installLedger) allBaseStepsDone() bool {
	if len(l.Steps) != len(baseInstallStepKeys) {
		return false
	}
	for _, key := range baseInstallStepKeys {
		if !l.done(key) {
			return false
		}
	}
	return true
}

func (l *installLedger) done(key string) bool {
	_, ok := l.Steps[key]
	return ok
}

func (l *installLedger) markDone(key, version string) error {
	if !baseStepKeySet()[key] {
		return fmt.Errorf("cannot record unknown base-install step %q", key)
	}
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("cannot record step %q with empty version", key)
	}
	l.Steps[key] = ledgerEntry{
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
		Version:     version,
	}
	return nil
}

func (l *installLedger) setDbCache(v int) error {
	if !validDbCache(v) {
		return fmt.Errorf("invalid db cache %d", v)
	}
	if l.Context.DbCacheMB != nil && *l.Context.DbCacheMB != v {
		return fmt.Errorf("db cache is already recorded as %d, refusing %d",
			*l.Context.DbCacheMB, v)
	}
	l.Context.DbCacheMB = &v
	return nil
}

func (l *installLedger) markComplete() error {
	if !l.allBaseStepsDone() {
		return fmt.Errorf("cannot complete lifecycle before every base step is recorded")
	}
	l.Status = ledgerComplete
	return nil
}

func readLedger(path string) (*installLedger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read install ledger %s: %w", path, err)
	}
	l, err := parseLedger(data)
	if err != nil {
		return nil, fmt.Errorf("invalid install ledger %s: %w", path, err)
	}
	return l, nil
}

// save publishes the complete new ledger, then synchronizes the containing
// directory so a reported success does not depend only on cached rename state.
func (l *installLedger) save(path string) (retErr error) {
	if err := validateLedger(l); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".install-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp ledger: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmp != nil {
			tmp.Close()
		}
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		tmp = nil
		return err
	}
	tmp = nil
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open ledger directory: %w", err)
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return fmt.Errorf("sync ledger directory: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close ledger directory: %w", err)
	}
	return nil
}

// rejectDuplicateJSONKeys walks the JSON token stream and rejects duplicate
// names at every object depth. encoding/json otherwise silently keeps the last
// value, which would make lifecycle authority ambiguous.
func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var stack []jsonFrame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode ledger tokens: %w", err)
		}
		if len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.object && top.expectKey {
				if delim, ok := tok.(json.Delim); ok && delim == '}' {
					// Empty object or end of the current object.
				} else {
					key, ok := tok.(string)
					if !ok {
						return fmt.Errorf("invalid JSON object key")
					}
					if top.keys[key] {
						return fmt.Errorf("duplicate JSON field %q", key)
					}
					top.keys[key] = true
					top.expectKey = false
					continue
				}
			}
		}
		switch d := tok.(type) {
		case json.Delim:
			switch d {
			case '{':
				stack = append(stack, jsonFrame{object: true, expectKey: true,
					keys: map[string]bool{}})
			case '[':
				stack = append(stack, jsonFrame{})
			case '}', ']':
				if len(stack) == 0 {
					return fmt.Errorf("unbalanced JSON delimiter")
				}
				stack = stack[:len(stack)-1]
				finishJSONValue(stack)
			}
		default:
			finishJSONValue(stack)
		}
	}
}

type jsonFrame struct {
	object    bool
	expectKey bool
	keys      map[string]bool
}

func finishJSONValue(stack []jsonFrame) {
	if len(stack) > 0 && stack[len(stack)-1].object {
		stack[len(stack)-1].expectKey = true
	}
}
