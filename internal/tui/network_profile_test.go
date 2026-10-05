package tui

import (
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/config"
)

func TestLightningInvoicePrefilterUsesInstalledProfile(t *testing.T) {
	for _, network := range config.SupportedNetworks() {
		cfg := config.Default()
		cfg.Network = network
		profile, err := cfg.NetworkConfig()
		if err != nil {
			t.Fatal(err)
		}
		screen := NewSendScreen(&ScreenContext{Cfg: cfg, State: &RuntimeState{WalletKnown: true, WalletExists: true}})
		screen.sendInput.SetValue(profile.InvoicePrefix + "1example")
		_, cmd := screen.preparePaymentConfirmation()
		if cmd == nil || screen.inputError != "" {
			t.Errorf("%s invoice rejected before LND: %q", network, screen.inputError)
		}

		foreign := "lnbc1example"
		if network == config.NetworkMainnet {
			foreign = "lntb1example"
		}
		screen.sendInput.SetValue(foreign)
		_, cmd = screen.preparePaymentConfirmation()
		if cmd != nil || !strings.Contains(screen.inputError, "not for") {
			t.Errorf("%s foreign invoice result cmd=%v error=%q",
				network, cmd != nil, screen.inputError)
		}
	}
}

func TestTestingNetworkBanner(t *testing.T) {
	for _, network := range config.SupportedNetworks() {
		cfg := config.Default()
		cfg.Network = network
		m := Model{cfg: cfg}
		banner := m.renderNetworkBanner()
		if network == config.NetworkMainnet && banner != "" {
			t.Errorf("mainnet banner = %q", banner)
		}
		if network != config.NetworkMainnet &&
			(!strings.Contains(banner, "TESTING NETWORK") ||
				!strings.Contains(banner, "no mainnet value")) {
			t.Errorf("%s banner = %q", network, banner)
		}
	}
}
