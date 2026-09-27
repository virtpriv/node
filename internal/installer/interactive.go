package installer

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/virtualprivatenode/vpn/internal/loginpassword"
	"github.com/virtualprivatenode/vpn/internal/system"
)

// InstallFrontend presents one root installation session. Returning requests
// a graceful stop; the installer joins admitted work before releasing its lock.
type InstallFrontend func(InstallView, *InstallSession) (openConsole bool, err error)

// InstallView contains observations and presentation data, never ledger access
// or executable installation steps.
type InstallView struct {
	NeedIdentity, NeedHardware bool
	Hardware, Minimum          Hardware
	DBCacheChoices             []int
	RecommendedDBCache         int
	Steps                      []InstallStepView
	Address                    string
}

// InstallStepView describes a step without granting execution authority.
type InstallStepView struct {
	Name   string
	Status StepStatus
	Err    error
}

// InteractiveInput contains the operator's confirmed installation choices.
type InteractiveInput struct {
	Password  loginpassword.Password
	DBCacheMB int
}

// InstallEvent reports ordered progress or the engine's terminal outcome.
type InstallEvent struct {
	Index         int
	Status        StepStatus
	Err           error
	Final         bool
	Result        RunResult
	CompletionErr error
}

// InstallSession owns one execution, independently of progress consumption.
// Stop prevents admission of another step; it does not kill an active subprocess
// or undo its changes. Forced process death still relies on lifecycle resume.
type InstallSession struct {
	mu                         sync.Mutex
	started, stopped           bool
	runner                     *stepRunner
	dec                        *InstallDecisions
	persistDBCache             func(int) error
	complete                   func() error
	needIdentity, needHardware bool
	events                     chan InstallEvent
	done                       chan struct{}
	result                     RunResult
	completionErr              error
}

func newInstallSession(r *stepRunner, dec *InstallDecisions,
	persistDBCache func(int) error, complete func() error,
) *InstallSession {
	return &InstallSession{
		runner: r, dec: dec, persistDBCache: persistDBCache, complete: complete,
		needIdentity: r.willRun("identity.access"),
		needHardware: r.willRun("btc.install") && r.ledger.Context.DbCacheMB == nil,
		// Each step emits running and finished, followed by one final event.
		// A closed or failed frontend cannot block the installation worker.
		events: make(chan InstallEvent, 2*len(r.steps)+1), done: make(chan struct{}),
		result: classifyRun(r.steps, false, false, 0),
	}
}

// Start validates and freezes choices before admitting the first step. It may
// be retried after a decision error, but a started or stopped session is final.
func (s *InstallSession) Start(input InteractiveInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return errors.New("installation stop requested")
	}
	if s.started {
		return errors.New("installation already started")
	}
	var password loginpassword.Password
	if s.needIdentity {
		var err error
		password, err = loginpassword.New(input.Password.Text())
		if err != nil {
			return err
		}
	}
	if s.needHardware {
		if !slices.Contains(dbCacheChoices, input.DBCacheMB) {
			return errors.New("unsupported database cache choice")
		}
		if err := s.persistDBCache(input.DBCacheMB); err != nil {
			return err
		}
	}
	if s.needIdentity {
		s.dec.Password = password
	}
	s.started = true
	go s.run()
	return nil
}

// Stop requests a stop after the admitted step and its ledger publication.
func (s *InstallSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if !s.started {
		s.events <- InstallEvent{Final: true, Result: s.result}
		close(s.events)
		close(s.done)
	}
}

// Events supplies one ordered stream for the frontend's single observer.
func (s *InstallSession) Events() <-chan InstallEvent { return s.events }

func (s *InstallSession) close() (RunResult, error) {
	s.Stop()
	<-s.done
	return s.result, s.completionErr
}

func (s *InstallSession) run() {
	defer close(s.done)
	defer close(s.events)
	defer func() { s.dec.Password = loginpassword.Password{} }()
	defer func() { s.events <- InstallEvent{Final: true, Result: s.result, CompletionErr: s.completionErr} }()
	for i := range s.runner.steps {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			s.result = classifyRun(s.runner.steps, false, false, i)
			return
		}
		s.events <- InstallEvent{Index: i, Status: StepRunning}
		s.mu.Unlock()
		skipped, err := s.runner.runIndex(i)
		status := StepDone
		if skipped {
			status = StepSkipped
		}
		if err != nil {
			status = StepFailed
		}
		s.runner.steps[i].Status = status
		s.runner.steps[i].Err = err
		s.events <- InstallEvent{Index: i, Status: status, Err: err}
		if err != nil {
			s.result = classifyRun(s.runner.steps, false, true, i)
			return
		}
	}
	// Once every step is recorded, finish publication even if observation or
	// a stop request raced with the last step. Completion is engine-owned.
	s.result = classifyRun(s.runner.steps, true, false, len(s.runner.steps))
	if err := s.complete(); err != nil {
		s.completionErr = fmt.Errorf("install steps complete but finalization failed: %w; run sudo vpn install again to retry", err)
	}
}

func runInstallInteractive(s *InstallSession, view InstallView, frontend InstallFrontend) (result RunResult, openConsole bool, err error) {
	// The join also runs on a frontend panic, before the caller releases its lock.
	defer func() {
		var completionErr error
		result, completionErr = s.close()
		err = errors.Join(err, completionErr)
		openConsole = openConsole && err == nil && result.Outcome == RunComplete
	}()
	openConsole, err = frontend(view, s)
	return
}

func installView(s *InstallSession) InstallView {
	hw := DetectHardware()
	view := InstallView{
		NeedIdentity: s.needIdentity, NeedHardware: s.needHardware,
		Hardware:       hw,
		Minimum:        Hardware{RAMMB: requiredRAMMB, DiskTotalGB: requiredDiskGB, Cores: requiredCores},
		DBCacheChoices: slices.Clone(dbCacheChoices), RecommendedDBCache: RecommendDbCache(hw.RAMMB),
		Address: system.PublicIPv4(),
	}
	for _, step := range s.runner.steps {
		view.Steps = append(view.Steps, InstallStepView{Name: step.Name})
	}
	return view
}
