package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/autounlock"
	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/p2p"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/servicecontrol"
	"github.com/virtpriv/node/internal/update"
)

// kind is one kind of job: how vpnd accepts it and what its service runs.
type kind struct {
	rule Rule
	// accept validates the request's parameters in vpnd, before anything is
	// recorded. It returns the job's title, the parameters to keep in its
	// record, and a secret for the job's credential copy.
	accept func(params json.RawMessage) (title string, keep json.RawMessage, secret string, err error)
	// steps is the kind's step list, or none.
	steps func(keep json.RawMessage) []string
	// run does the work in the job's service and returns its typed result.
	run func(c *runCtx, keep json.RawMessage) (any, error)
}

// PrepareResult is a Prepare Update job's result. The review itself stays in
// the update folder, where Prepare writes it, so the record stays small.
type PrepareResult struct {
	Version     string `json:"version"`
	Digest      string `json:"digest,omitempty"`
	Token       string `json:"token,omitempty"`
	UpdateFirst string `json:"update_first,omitempty"`
}

// credentialName is the auto-unlock password's name in the job's
// credential folder.
const credentialName = "password"

var kinds = map[string]kind{
	api.KindPackageUpdate: {
		accept: noParams("Update Packages"),
		steps:  func(json.RawMessage) []string { return api.PackageUpdateStepNames() },
		run: func(c *runCtx, _ json.RawMessage) (any, error) {
			return nil, host.UpdatePackages(c.step)
		},
	},
	api.KindPrepareUpdate: {
		rule: RuleJobs,
		accept: func(raw json.RawMessage) (string, json.RawMessage, string, error) {
			var p api.PrepareUpdateParams
			if err := decode(raw, &p); err != nil {
				return "", nil, "", err
			}
			if !release.ValidVersion(p.Version) {
				return "", nil, "", fmt.Errorf("%q is not a valid release version", p.Version)
			}
			keep, err := json.Marshal(p)
			return "Prepare update to v" + p.Version, keep, "", err
		},
		run: func(c *runCtx, raw json.RawMessage) (any, error) {
			var p api.PrepareUpdateParams
			if err := decode(raw, &p); err != nil {
				return nil, err
			}
			review, err := update.Prepare(c.version, p.Version)
			if err != nil {
				return nil, err
			}
			return PrepareResult{Version: p.Version, Digest: review.Digest, Token: review.Token, UpdateFirst: review.UpdateFirst}, nil
		},
	},
	api.KindServiceAction: {
		accept: func(raw json.RawMessage) (string, json.RawMessage, string, error) {
			request, keep, err := serviceRequest(raw)
			if err != nil {
				return "", nil, "", err
			}
			action := request.Action()
			return strings.ToUpper(action[:1]) + action[1:] + " " + request.Service(), keep, "", nil
		},
		run: func(_ *runCtx, raw json.RawMessage) (any, error) {
			request, _, err := serviceRequest(raw)
			if err != nil {
				return nil, err
			}
			completion, err := host.ControlService(request)
			if err != nil {
				return nil, err
			}
			// LND may replace its TLS certificate at startup. Publish the
			// matching staged copy before the job reports completion.
			if request.Service() == "lnd" && request.Action() != "stop" {
				if err := Restage(api.KindServiceAction); err != nil {
					return nil, fmt.Errorf("lnd is active, credential refresh failed: %w", err)
				}
			}
			return completion, nil
		},
	},
	api.KindSyncthingInstall: {
		accept: noParams("Install Syncthing"),
		steps: func(json.RawMessage) []string {
			return api.SyncthingInstallStepNames(component.SyncthingVersion)
		},
		run: func(c *runCtx, _ json.RawMessage) (any, error) {
			return nil, host.InstallSyncthing(func() error {
				return Restage(api.KindSyncthingInstall)
			}, c.step)
		},
	},
	api.KindP2PUpgrade: {
		accept: func(raw json.RawMessage) (string, json.RawMessage, string, error) {
			_, keep, err := p2pRequest(raw)
			return "Upgrade P2P to Hybrid", keep, "", err
		},
		steps: func(json.RawMessage) []string { return api.UpgradeP2PToHybridStepNames() },
		run: func(c *runCtx, raw json.RawMessage) (any, error) {
			request, _, err := p2pRequest(raw)
			if err != nil {
				return nil, err
			}
			return nil, host.UpgradeP2PToHybrid(request, func() error {
				return Restage(api.KindP2PUpgrade)
			}, c.step)
		},
	},
	api.KindAutoUnlockOn: {
		accept: func(raw json.RawMessage) (string, json.RawMessage, string, error) {
			var p api.AutoUnlockOnParams
			if err := decode(raw, &p); err != nil {
				return "", nil, "", err
			}
			password, err := autounlock.NewPassword(p.Password)
			if err != nil {
				return "", nil, "", err
			}
			// The password goes only to the job's credential copy.
			return "Turn on auto-unlock", nil, password.Text(), nil
		},
		run: func(_ *runCtx, _ json.RawMessage) (any, error) {
			password, err := readCredential()
			if err != nil {
				return nil, err
			}
			return host.SetupAutoUnlock(password)
		},
	},
	api.KindAutoUnlockOff: {
		accept: noParams("Turn off auto-unlock"),
		run: func(_ *runCtx, _ json.RawMessage) (any, error) {
			return host.DisableAutoUnlock()
		},
	},
	api.KindReboot: {
		accept: noParams("Reboot"),
		run: func(_ *runCtx, _ json.RawMessage) (any, error) {
			if err := host.RequestReboot(); err != nil {
				return nil, err
			}
			return api.RebootResult{Accepted: true}, nil
		},
	},
	api.KindSSHPasswordLogin: {
		accept: func(raw json.RawMessage) (string, json.RawMessage, string, error) {
			var p api.SSHPasswordLoginParams
			if err := decode(raw, &p); err != nil {
				return "", nil, "", err
			}
			keep, err := json.Marshal(p)
			title := "Turn on SSH password login"
			if p.PasswordAuthDisabled {
				title = "Turn off SSH password login"
			}
			return title, keep, "", err
		},
		run: func(_ *runCtx, raw json.RawMessage) (any, error) {
			var p api.SSHPasswordLoginParams
			if err := decode(raw, &p); err != nil {
				return nil, err
			}
			// The fresh key guard, the validate and restore sequence and the
			// effective state check live at the privileged write boundary.
			return nil, host.RebuildSSHHardeningConfig(p.PasswordAuthDisabled)
		},
	},
}

// Validate checks a start request in vpnd. It returns the job's title, the
// parameters its record keeps, a secret for the job's credential copy and
// the busy rule of its kind.
func Validate(kindName string, params json.RawMessage) (title string, keep json.RawMessage, secret string, rule Rule, err error) {
	k, ok := kinds[kindName]
	if !ok {
		return "", nil, "", 0, fmt.Errorf("unknown job kind %q", kindName)
	}
	title, keep, secret, err = k.accept(params)
	return title, keep, secret, k.rule, err
}

func noParams(title string) func(json.RawMessage) (string, json.RawMessage, string, error) {
	return func(raw json.RawMessage) (string, json.RawMessage, string, error) {
		return title, nil, "", RejectParams(raw)
	}
}

func serviceRequest(raw json.RawMessage) (servicecontrol.Request, json.RawMessage, error) {
	var p api.ServiceActionParams
	if err := decode(raw, &p); err != nil {
		return servicecontrol.Request{}, nil, err
	}
	request, err := servicecontrol.New(p.Unit, p.Action)
	if err != nil {
		return servicecontrol.Request{}, nil, err
	}
	keep, err := json.Marshal(p)
	return request, keep, err
}

func p2pRequest(raw json.RawMessage) (p2p.UpgradeRequest, json.RawMessage, error) {
	var p api.UpgradeP2PParams
	if err := decode(raw, &p); err != nil {
		return p2p.UpgradeRequest{}, nil, err
	}
	request, err := p2p.NewUpgradeRequest(p.ExpectedIPv4)
	if err != nil {
		return p2p.UpgradeRequest{}, nil, err
	}
	keep, err := json.Marshal(p)
	return request, keep, err
}

// readCredential reads the password systemd copied for this job only.
func readCredential() (autounlock.Password, error) {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return autounlock.Password{}, errors.New("the job has no credential folder")
	}
	b, err := os.ReadFile(filepath.Join(dir, credentialName))
	if err != nil {
		return autounlock.Password{}, err
	}
	return autounlock.NewPassword(string(b))
}

// decode unmarshals parameters strictly: an unknown field is an error, so a
// client and server drift surfaces as a refusal instead of an ignored option.
func decode(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("invalid parameters: %v", err)
	}
	return nil
}

// RejectParams refuses parameters where a request takes none.
func RejectParams(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return errors.New("this request accepts no parameters")
}
