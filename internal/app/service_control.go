package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/servicecontrol"
)

// ServiceActionParams are the parameters of a validated service request.
func ServiceActionParams(request servicecontrol.Request) api.ServiceActionParams {
	return api.ServiceActionParams{Unit: request.Service(), Action: request.Action()}
}

// VerifyServiceAction turns a completed service job into its outcome: it is
// completed only when systemd's completion matches the request.
func VerifyServiceAction(request servicecontrol.Request, u JobUpdate) JobUpdate {
	if u.Outcome != JobCompleted {
		return u
	}
	var completion servicecontrol.Completion
	err := json.Unmarshal(u.Job.Result, &completion)
	if err == nil {
		err = request.Verify(completion)
	}
	if err != nil {
		u.Outcome, u.Err = JobUnconfirmed, err
	}
	return u
}

// ServiceRequestOf rebuilds a service job's request from its title, such as
// "Restart lnd", for a job this TUI did not start.
func ServiceRequestOf(job api.Job) (servicecontrol.Request, error) {
	action, service, ok := strings.Cut(job.Title, " ")
	if !ok || job.Kind != api.KindServiceAction {
		return servicecontrol.Request{}, fmt.Errorf("not a service job: %q", job.Title)
	}
	return servicecontrol.New(service, strings.ToLower(action))
}
