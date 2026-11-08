package host

import (
	"errors"
	"testing"
)

func TestConfigureFail2banRefusesBeforeWritingWhenSSHObservationFails(t *testing.T) {
	oldObserve, oldWrite, oldRun := observeSSHForFail2ban, writeFail2banJail, runFail2banCommand
	t.Cleanup(func() {
		observeSSHForFail2ban, writeFail2banJail, runFail2banCommand = oldObserve, oldWrite, oldRun
	})
	observeSSHForFail2ban = func() (SSHObservation, error) {
		return SSHObservation{}, errors.New("sshd unavailable")
	}
	mutated := false
	writeFail2banJail = func([]byte) error { mutated = true; return nil }
	runFail2banCommand = func(...string) error { mutated = true; return nil }
	if err := ConfigureFail2ban(); err == nil {
		t.Fatal("fail2ban setup accepted unavailable SSH observation")
	}
	if mutated {
		t.Fatal("fail2ban setup wrote or restarted before SSH observation succeeded")
	}
}
