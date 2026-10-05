package helperd

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/virtpriv/node/internal/helper"
)

func TestRebootDispatchRejectsParamsAndAcknowledgesHostAcceptance(t *testing.T) {
	previous := requestReboot
	t.Cleanup(func() { requestReboot = previous })
	failure := errors.New("injected systemctl refusal")
	calls := 0
	requestReboot = func() error { calls++; return failure }
	handler := verbs["reboot"].handler
	if result, err := handler(&verbCtx{}, json.RawMessage(`{"force":true}`)); err == nil || result != nil || calls != 0 {
		t.Fatalf("untrusted parameters reached host: %v", err)
	}
	if result, err := handler(&verbCtx{}, nil); !errors.Is(err, failure) || result != nil || calls != 1 {
		t.Fatalf("host failure was acknowledged: %+v, %v, calls %d", result, err, calls)
	}
	requestReboot = func() error { calls++; return nil }
	result, err := handler(&verbCtx{}, nil)
	if err != nil || result != (helper.RebootResult{Accepted: true}) || calls != 2 {
		t.Fatalf("acknowledgement preceded host acceptance: %+v, %v, calls %d", result, err, calls)
	}
}
