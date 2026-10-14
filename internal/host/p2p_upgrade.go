package host

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/p2p"
	"github.com/virtpriv/node/internal/servicecontrol"
	"github.com/virtpriv/node/internal/system"
)

type p2pUpgradeOps struct {
	load      func() (*config.AppConfig, error)
	address   func(context.Context) (string, error)
	firewall  func() error
	configure func(*config.AppConfig, string) error
	allow     func() error
	restart   func() error
	verifyTLS func(string) error
	stage     func() error
	save      func(*config.AppConfig) error
}

// UpgradeP2PToHybrid completes accepted root work independently of the terminal.
// The helper supplies its fixed freshness-matrix action, keeping TLS staging
// inside the transition's completion boundary and before desired-state publication.
func UpgradeP2PToHybrid(request p2p.UpgradeRequest, stage func() error, progress func(int)) error {
	if os.Geteuid() != 0 {
		return nothingChanged(errors.New("hybrid P2P requires the root helper"))
	}
	return upgradeP2PToHybrid(request, p2pUpgradeOps{
		load: config.Load, address: system.ReadPublicIPv4,
		firewall: RequireActiveFirewall, configure: WriteLNDConfig,
		allow: allowHybridP2PFirewallRules,
		restart: func() error {
			req, err := servicecontrol.New("lnd", "restart")
			if err != nil {
				return err
			}
			_, err = ControlService(req)
			return err
		},
		verifyTLS: verifyLNDTLSIPSAN, stage: stage, save: config.Save,
	}, progress)
}

func upgradeP2PToHybrid(request p2p.UpgradeRequest, ops p2pUpgradeOps, progress func(int)) error {
	// Every refusal before the steps, and the firewall check, come before any
	// change to the node.
	if request.Address() == "" {
		return nothingChanged(errors.New("review the public IPv4 address before upgrading P2P"))
	}
	cfg, err := ops.load()
	if err != nil {
		return nothingChanged(fmt.Errorf("read node configuration: %w", err))
	}
	if cfg.P2PMode != "tor" {
		return nothingChanged(fmt.Errorf("hybrid P2P requires authoritative mode tor, current mode is %q", cfg.P2PMode))
	}
	// Read freshly for this invocation. A cached observation from another
	// process or an earlier request cannot authorize publication of this IP.
	address, err := ops.address(context.Background())
	if err != nil {
		return nothingChanged(fmt.Errorf("observe public IPv4: %w", err))
	}
	observed, err := p2p.NewUpgradeRequest(address)
	if err != nil {
		return nothingChanged(err)
	}
	if observed != request {
		return nothingChanged(errors.New("public IPv4 changed. Reopen P2P Upgrade and review the address again"))
	}
	proposed := *cfg
	proposed.P2PMode = "hybrid"
	steps := []struct {
		name     string
		run      func() error
		noChange bool
	}{
		{"check active firewall", ops.firewall, true},
		{"update LND configuration", func() error { return ops.configure(&proposed, observed.Address()) }, false},
		{"add Hybrid P2P firewall rules", ops.allow, false},
		{"restart LND", ops.restart, false},
		// LND owns TLS regeneration. Never delete its key or certificate here.
		{"verify LND TLS IP certificate", func() error { return ops.verifyTLS(observed.Address()) }, false},
		{"restage LND TLS certificate", ops.stage, false},
		{"publish P2P setting", func() error { return ops.save(&proposed) }, false},
	}
	for i, step := range steps {
		if err := step.run(); err != nil {
			// Earlier live changes can remain. Failure is not a rollback receipt.
			err = fmt.Errorf("%s: %w", step.name, err)
			if step.noChange {
				return nothingChanged(err)
			}
			return err
		}
		progress(i)
	}
	return nil
}
