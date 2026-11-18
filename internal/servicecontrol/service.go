// Package servicecontrol defines the services and actions managed by the node.
package servicecontrol

import "fmt"

type Request struct {
	service string
	action  string
}

func New(service, action string) (Request, error) {
	if Unit(service) == "" {
		return Request{}, fmt.Errorf("service %q is not managed here", service)
	}
	switch action {
	case "start", "stop", "restart":
		return Request{service: service, action: action}, nil
	default:
		return Request{}, fmt.Errorf("action %q is not supported", action)
	}
}

func (r Request) Service() string { return r.service }
func (r Request) Action() string  { return r.action }
func (r Request) Unit() string    { return Unit(r.service) }

// Unit keeps control, observations and logs on the same unit. Debian's Tor
// wrapper can remain active after the default worker has stopped.
func Unit(service string) string {
	switch service {
	case "tor":
		return "tor@default.service"
	default:
		return ""
	}
}

// Completion is evidence of a postcondition, not daemon or network readiness.
type Completion struct {
	Unit   string `json:"unit"`
	Action string `json:"action"`
	State  string `json:"state"`
}

func (r Request) Verify(c Completion) error {
	if r.Unit() == "" || c.Unit != r.Unit() || c.Action != r.Action() {
		return fmt.Errorf("missing or mismatched service completion")
	}
	if r.action == "stop" && c.State == "inactive" {
		return nil
	}
	if r.action != "stop" && c.State == "active" {
		return nil
	}
	return fmt.Errorf("%s state after %s: %s", c.Unit, c.Action, c.State)
}
