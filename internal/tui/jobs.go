package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
)

// jobVisible is decision 3 of ADR-0016's lists amendment: a running job
// always shows on the screen of its kind, a completed one for 24 hours after
// it ended, a failed or unknown one until a newer job of its kind.
func jobVisible(j api.Job, now time.Time) bool {
	if !j.Finished() {
		return true
	}
	if j.State == api.JobDone {
		return now.Sub(j.Ended) < 24*time.Hour
	}
	return true
}

// jobStepLine names a running job's step, for kinds that have steps.
func jobStepLine(j api.Job) string {
	if j.Finished() || j.StepNumber < 1 || j.StepNumber > len(j.Steps) {
		return ""
	}
	return fmt.Sprintf("Step %d of %d: %s", j.StepNumber, len(j.Steps), j.Steps[j.StepNumber-1])
}

// finalUpdate reads a finished job as the last update of a watch.
func finalUpdate(j api.Job) app.JobUpdate {
	return app.JobUpdate{Job: j, Outcome: app.Outcome(j), Final: true}
}

// jobReason gives an update's error on one printable line, for the screens
// that show it after their own words.
func jobReason(u app.JobUpdate) string {
	err := u.Error()
	if err == nil {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		if r == '—' {
			return ':'
		}
		return r
	}, err.Error())
}

// withReason joins a screen's own words and a reason. A reason that is a
// sentence already, such as vpnd's busy refusal, follows a full stop. Any
// other, such as a Go error that starts lowercase, follows a colon, the join
// before a cause. Its first letter is never changed: it can be a program's
// name such as systemd-run.
func withReason(lead, reason string) string {
	if reason == "" {
		return lead + "."
	}
	if r := []rune(reason)[0]; unicode.IsUpper(r) {
		return lead + ". " + sentence(reason)
	}
	return lead + ": " + sentence(reason)
}

// sentence ends text with a full stop unless it ends a sentence already.
func sentence(text string) string {
	if text == "" || strings.HasSuffix(text, ".") || strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") {
		return text
	}
	return text + "."
}

// observationLost reports an update whose unknown outcome comes from this
// TUI losing vpnd's answers, not from a job that ended without a result. The
// job may then still be running.
func observationLost(u app.JobUpdate) bool {
	return u.Outcome == app.JobUnconfirmed && u.Err != nil
}

// restoredJobsMsg carries, at the TUI's start, the newest job of each kind
// the System screens start.
type restoredJobsMsg struct {
	jobs map[string]api.Job
}

// restoreSystemJobsCmd reads the newest Update Packages, Reboot and service
// job, so a TUI opened during or after one shows it (S2).
func restoreSystemJobsCmd(c *ScreenContext) tea.Cmd {
	runner := c.jobs()
	return func() tea.Msg {
		found := map[string]api.Job{}
		for _, kind := range []string{api.KindPackageUpdate, api.KindReboot, api.KindServiceAction} {
			if j, ok, err := runner.Latest(kind); err == nil && ok && jobVisible(j, time.Now()) {
				found[kind] = j
			}
		}
		return restoredJobsMsg{jobs: found}
	}
}

// restoreSystemJobs puts the restored jobs on the System screen, unless this
// TUI already started one of the same kind.
func (m *Model) restoreSystemJobs(msg restoredJobsMsg) tea.Cmd {
	s, ok := m.sectionScreens[secSystem].(*SystemHomeScreen)
	if !ok {
		return nil
	}
	var cmds []tea.Cmd
	if j, ok := msg.jobs[api.KindPackageUpdate]; ok && s.pkgPending == nil && s.pkgResult == "" {
		if j.Finished() {
			s.pkgResult = packageResultText(finalUpdate(j))
		} else {
			attempt := &packageAttempt{updates: m.screenCtx.jobs().Watch(j.ID), job: j}
			s.pkgPending = attempt
			cmds = append(cmds, watchPackageCmd(s, attempt))
		}
	}
	if j, ok := msg.jobs[api.KindReboot]; ok && s.rebootPending == nil && s.rebootResult == "" {
		if j.Finished() {
			s.applyRebootResult(finalUpdate(j))
		} else {
			attempt := &rebootAttempt{updates: m.screenCtx.jobs().Watch(j.ID)}
			s.rebootPending = attempt
			cmds = append(cmds, watchRebootCmd(s, attempt))
		}
	}
	if j, ok := msg.jobs[api.KindServiceAction]; ok && s.svcPending == nil && s.svcResult == "" {
		if request, err := app.ServiceRequestOf(j); err == nil {
			if j.Finished() {
				s.svcResult = serviceResultText(request, app.VerifyServiceAction(request, finalUpdate(j)))
			} else {
				attempt := &serviceAttempt{request: request, updates: m.screenCtx.jobs().Watch(j.ID)}
				s.svcPending = attempt
				cmds = append(cmds, watchServiceCmd(s, attempt))
			}
		}
	}
	return tea.Batch(cmds...)
}
