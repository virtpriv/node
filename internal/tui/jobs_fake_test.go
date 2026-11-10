package tui

import (
	"encoding/json"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/servicecontrol"
)

// fakeJobs stands for vpnd's jobs. Each Start gets a fresh channel, kept in
// results for the test to send the job's updates on.
type fakeJobs struct {
	calls   int
	kinds   []string
	params  []any
	results chan app.JobUpdate
	latest  map[string]api.Job
	watched []string
}

func (f *fakeJobs) Start(kind string, params any) <-chan app.JobUpdate {
	f.calls++
	f.kinds = append(f.kinds, kind)
	f.params = append(f.params, params)
	f.results = make(chan app.JobUpdate, 1)
	return f.results
}

func (f *fakeJobs) Watch(id string) <-chan app.JobUpdate {
	f.watched = append(f.watched, id)
	f.results = make(chan app.JobUpdate, 1)
	return f.results
}

func (f *fakeJobs) Latest(kind string) (api.Job, bool, error) {
	j, ok := f.latest[kind]
	return j, ok, nil
}

func (*fakeJobs) Close() {}

func done(outcome app.JobOutcome, err error) app.JobUpdate {
	return app.JobUpdate{Outcome: outcome, Final: true, Err: err}
}

// serviceDone is the completed job of the last service request, with the
// completion systemd reports.
func (f *fakeJobs) serviceDone() app.JobUpdate {
	p := f.params[len(f.params)-1].(api.ServiceActionParams)
	request, err := servicecontrol.New(p.Unit, p.Action)
	if err != nil {
		panic(err)
	}
	state := "active"
	if p.Action == "stop" {
		state = "inactive"
	}
	raw, _ := json.Marshal(servicecontrol.Completion{Unit: request.Unit(), Action: p.Action, State: state})
	return app.JobUpdate{Job: api.Job{Kind: api.KindServiceAction, State: api.JobDone, Result: raw}, Outcome: app.JobCompleted, Final: true}
}

// service is the i'th service request sent.
func (f *fakeJobs) service(i int) api.ServiceActionParams {
	return f.params[i].(api.ServiceActionParams)
}
