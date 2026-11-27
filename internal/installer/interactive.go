package installer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/loginpassword"
	"github.com/virtpriv/node/internal/system"
)

// InstallFrontend presents one root installation session. Returning requests
// a graceful stop, and the installer joins admitted work before releasing its lock.
// The frontend never opens the node TUI: the owner reaches it over SSH.
type InstallFrontend func(InstallView, *InstallSession) error

// InstallView contains observations and presentation data, never ledger access
// or executable installation steps.
type InstallView struct {
	NeedIdentity bool
	Steps        []InstallStepView
	Address      string
	// RootLoginOff is set when the install record already shows root's SSH
	// login turned off.
	RootLoginOff bool
}

// InstallStepView describes a step without granting execution authority.
type InstallStepView struct {
	Name   string
	Status StepStatus
	Err    error
}

// InteractiveInput holds the owner's password, the one choice the interactive
// install asks for.
type InteractiveInput struct {
	Password loginpassword.Password
}

// InstallEvent reports ordered progress or the engine's terminal outcome.
type InstallEvent struct {
	Index         int
	Status        StepStatus
	Err           error
	Final         bool
	Result        RunResult
	CompletionErr error
	// RootLoginOff, on the final event, is set when the install record
	// shows root's SSH login turned off. RootLoginMayBeOff is set when the
	// SSH step failed after it may have turned it off.
	RootLoginOff, RootLoginMayBeOff bool
}

// InstallSession owns one execution, independently of progress consumption.
// Stop asks for a pause. It ends a step's wait and admits no other step. It
// does not kill an active subprocess or undo its changes. Forced process
// death still relies on lifecycle resume.
type InstallSession struct {
	mu               sync.Mutex
	started, stopped bool
	// ctx ends when Stop is called.
	ctx           context.Context
	cancel        context.CancelFunc
	runner        *stepRunner
	dec           *InstallDecisions
	complete      func() error
	needIdentity  bool
	events        chan InstallEvent
	done          chan struct{}
	result        RunResult
	completionErr error
	// began bounds the SSH log read, so only a login during this run counts.
	began time.Time
	// startErr is why the last Start was refused, kept for the shell if
	// the owner then leaves.
	startErr error
}

func newInstallSession(r *stepRunner, dec *InstallDecisions, complete func() error) *InstallSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &InstallSession{
		ctx: ctx, cancel: cancel,
		runner: r, dec: dec, complete: complete,
		needIdentity: r.willRun("identity.access"),
		// Each step emits at most two events, followed by one final event.
		// A closed or failed frontend cannot block the installation worker.
		events: make(chan InstallEvent, 2*len(r.steps)+1), done: make(chan struct{}),
		result: classifyRun(r.steps, false, false, 0),
		began:  time.Now(),
	}
}

// Start validates and freezes the password before admitting the first step. It
// may be retried after a refusal, but a started or stopped session is final.
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
			s.refuse(err)
			return err
		}
	}
	s.startErr = nil
	if s.needIdentity {
		s.dec.Password = password
	}
	s.started = true
	go s.run()
	return nil
}

// refuse keeps why Start was refused and logs it, because the screens point
// to the log. The caller holds s.mu.
func (s *InstallSession) refuse(err error) {
	s.startErr = err
	logger.Install("could not start the install: %v", err)
}

// Stop asks for a pause as soon as it is safe. That is at once when the
// running step waits, otherwise before its next command or after the step
// and its ledger publication.
func (s *InstallSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	s.cancel()
	if !s.started {
		s.events <- s.finalEvent()
		close(s.events)
		close(s.done)
	}
}

// finalEvent reports the outcome, and whether root's SSH login is now off or
// may be.
func (s *InstallSession) finalEvent() InstallEvent {
	off := s.runner.ledger.done(rootLoginOffStep)
	return InstallEvent{Final: true, Result: s.result, CompletionErr: s.completionErr,
		RootLoginOff:      off,
		RootLoginMayBeOff: !off && rootLoginMayBeOff(s.result.Err)}
}

// rootLoginMayBeOff reports whether a step's error says the SSH setting that
// turns root's login off may have been written.
func rootLoginMayBeOff(err error) bool {
	return errors.Is(err, host.ErrSSHSettingNotConfirmed)
}

// Events supplies one ordered stream for the frontend's single observer.
func (s *InstallSession) Events() <-chan InstallEvent { return s.events }

// Doing returns the step that last said what it is doing, counted from 0,
// and that line, empty once the step has ended. It is read, not sent as an
// event, so a frontend that is gone can never block a step.
func (s *InstallSession) Doing() (step int, text string) { return s.runner.Doing() }

// SSHLoginSeen reports whether SSH's log shows a login as vpn since this run
// began. A log that cannot be read is an error, never a login.
func (s *InstallSession) SSHLoginSeen() (bool, error) {
	return host.AdminLoginObserved(s.began)
}

func (s *InstallSession) close() (RunResult, error) {
	s.Stop()
	<-s.done
	return s.result, s.completionErr
}

func (s *InstallSession) run() {
	defer close(s.done)
	defer close(s.events)
	defer func() { s.dec.Password = loginpassword.Password{} }()
	defer func() { s.events <- s.finalEvent() }()
	s.runner.show = func(i int, status StepStatus, err error) {
		s.events <- InstallEvent{Index: i, Status: status, Err: err}
	}
	s.result = s.runner.run(s.ctx)
	if s.result.Outcome != RunComplete {
		return
	}
	// Once every step is recorded, finish publication even if observation or
	// a stop request raced with the last step. Completion is engine-owned.
	if err := s.complete(); err != nil {
		s.completionErr = fmt.Errorf("every step is done, but the install did not finish: %w. Run sudo vpn install again to finish it", err)
	}
}

// refusal is why the last Start was refused, nil after a start.
func (s *InstallSession) refusal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startErr
}

func runInstallInteractive(s *InstallSession, view InstallView, frontend InstallFrontend) (result RunResult, err error) {
	// The join also runs on a frontend panic, before the caller releases its lock.
	defer func() {
		var completionErr error
		result, completionErr = s.close()
		err = errors.Join(err, completionErr)
	}()
	return result, frontend(view, s)
}

func installView(s *InstallSession) InstallView {
	view := InstallView{
		NeedIdentity: s.needIdentity,
		Address:      system.PublicIPv4(),
		RootLoginOff: s.runner.ledger.done(rootLoginOffStep),
	}
	for _, step := range s.runner.steps {
		view.Steps = append(view.Steps, InstallStepView{Name: step.Name})
	}
	return view
}
