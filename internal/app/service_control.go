package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/servicecontrol"
)

type ServiceActionOutcome int

const (
	ServiceActionUnconfirmed ServiceActionOutcome = iota
	ServiceActionNotStarted
	ServiceActionCompleted
)

type ServiceActionResult struct {
	Outcome ServiceActionOutcome
	Err     error
}

// ServiceControls owns bounded helper observation for one terminal session.
// Close cancels and joins local work, not root mutations already accepted.
type ServiceControls struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers sync.WaitGroup
	call    func(context.Context, string, any, any) error
}

func NewServiceControls() *ServiceControls {
	ctx, cancel := context.WithCancel(context.Background())
	return &ServiceControls{ctx: ctx, cancel: cancel, call: callServiceHelper}
}

func callServiceHelper(ctx context.Context, verb string, params, result any) error {
	session, err := helper.StartContext(ctx, verb, params)
	if err != nil {
		return err
	}
	return session.Wait(result)
}

func (s *ServiceControls) request(ctx context.Context, request servicecontrol.Request) error {
	var completion servicecontrol.Completion
	if err := s.call(ctx, helper.VerbServiceAction,
		helper.ServiceActionParams{Unit: request.Service(), Action: request.Action()}, &completion); err != nil {
		return err
	}
	return request.Verify(completion)
}

func (s *ServiceControls) Close() {
	s.mu.Lock()
	s.cancel()
	s.mu.Unlock()
	s.workers.Wait()
}

func (s *ServiceControls) Control(request servicecontrol.Request) <-chan ServiceActionResult {
	results := make(chan ServiceActionResult, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.ctx.Err()
	if request.Unit() == "" {
		err = errors.New("service request was not validated")
	}
	if err != nil {
		results <- ServiceActionResult{Outcome: ServiceActionNotStarted, Err: err}
		close(results)
		return results
	}
	// Include queuing behind another helper verb. A timeout is not permission
	// to retry: the previous request may still be running on the host.
	ctx, cancel := context.WithTimeout(s.ctx, 35*time.Minute)
	s.workers.Go(func() {
		defer cancel()
		defer close(results)
		if err := ctx.Err(); err != nil {
			results <- ServiceActionResult{Outcome: ServiceActionNotStarted, Err: err}
			return
		}
		err := s.request(ctx, request)
		outcome := ServiceActionCompleted
		if err != nil {
			outcome = ServiceActionUnconfirmed
		}
		results <- ServiceActionResult{Outcome: outcome, Err: err}
	})
	return results
}
