package helperd

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/virtpriv/node/internal/helper"
)

func TestSyncthingInstallRejectsParametersBeforeHostDispatch(t *testing.T) {
	original := installSyncthing
	t.Cleanup(func() { installSyncthing = original })
	installSyncthing = func(func() error, func(int)) error {
		t.Fatal("parameter-bearing request reached root operation")
		return nil
	}
	for _, params := range []string{`{}`, `{"version":"2.2.0"}`, `{"path":"/tmp/config"}`, `true`, `[]`, `""`} {
		if result, err := verbSyncthingInstall(&verbCtx{}, json.RawMessage(params)); err == nil || result != nil {
			t.Fatalf("unexpected params accepted: %s", params)
		}
	}
}

func TestSyncthingInstallKeepsStagingInsideHostCompletion(t *testing.T) {
	originalInstall, originalRestage := installSyncthing, restageFacts
	t.Cleanup(func() { installSyncthing, restageFacts = originalInstall, originalRestage })
	failure := errors.New("staging failed")
	for _, stageErr := range []error{failure, nil} {
		calls := 0
		restageFacts = func(verb string) error {
			if verb != helper.VerbSyncthingInstall {
				t.Fatal("wrong credential freshness selection")
			}
			calls++
			return stageErr
		}
		installSyncthing = func(stage func() error, progress func(int)) error {
			if calls != 0 || progress == nil {
				t.Fatal("staging escaped host ordering or progress was lost")
			}
			return stage()
		}
		result, err := verbSyncthingInstall(&verbCtx{}, nil)
		if calls != 1 || result != nil || !errors.Is(err, stageErr) {
			t.Fatalf("result=%v error=%v staging calls=%d", result, err, calls)
		}
	}
}
