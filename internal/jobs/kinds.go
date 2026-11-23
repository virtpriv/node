package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/servicecontrol"
	"github.com/virtpriv/node/internal/update"
)

// kind is one kind of job: how vpnd accepts it and what its service runs.
type kind struct {
	rule Rule
	// accept validates the request's parameters in vpnd, before anything is
	// recorded. It returns the job's title and the parameters to keep in its
	// record.
	accept func(params json.RawMessage) (title string, keep json.RawMessage, err error)
	// run does the work in the job's service and returns its typed result.
	// It owns what is around its steps, such as a hold on restarts, and runs
	// the steps an owner watches through c.steps.
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

var kinds = map[string]kind{
	api.KindPackageUpdate: {
		accept: noParams("Update Packages"),
		run: func(c *runCtx, _ json.RawMessage) (any, error) {
			return nil, host.UpdatePackages(c.steps)
		},
	},
	api.KindPrepareUpdate: {
		rule: RuleJobs,
		accept: func(raw json.RawMessage) (string, json.RawMessage, error) {
			var p api.PrepareUpdateParams
			if err := decode(raw, &p); err != nil {
				return "", nil, err
			}
			if !release.ValidVersion(p.Version) {
				return "", nil, fmt.Errorf("%q is not a valid release version", p.Version)
			}
			keep, err := json.Marshal(p)
			return "Prepare update to v" + p.Version, keep, err
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
		accept: func(raw json.RawMessage) (string, json.RawMessage, error) {
			request, keep, err := serviceRequest(raw)
			if err != nil {
				return "", nil, err
			}
			action := request.Action()
			return strings.ToUpper(action[:1]) + action[1:] + " " + request.Service(), keep, nil
		},
		run: func(_ *runCtx, raw json.RawMessage) (any, error) {
			request, _, err := serviceRequest(raw)
			if err != nil {
				return nil, err
			}
			return host.ControlService(request)
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
		accept: func(raw json.RawMessage) (string, json.RawMessage, error) {
			var p api.SSHPasswordLoginParams
			if err := decode(raw, &p); err != nil {
				return "", nil, err
			}
			keep, err := json.Marshal(p)
			title := "Turn on SSH password login"
			if p.PasswordAuthDisabled {
				title = "Turn off SSH password login"
			}
			return title, keep, err
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
// parameters its record keeps and the busy rule of its kind.
func Validate(kindName string, params json.RawMessage) (title string, keep json.RawMessage, rule Rule, err error) {
	k, ok := kinds[kindName]
	if !ok {
		return "", nil, 0, fmt.Errorf("unknown job kind %q", kindName)
	}
	title, keep, err = k.accept(params)
	return title, keep, k.rule, err
}

func noParams(title string) func(json.RawMessage) (string, json.RawMessage, error) {
	return func(raw json.RawMessage) (string, json.RawMessage, error) {
		return title, nil, RejectParams(raw)
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
