package tui

import (
	"strings"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/theme"
)

func observationText[T any](o app.Observation[T], value string) string {
	if !o.Known() {
		return "unavailable"
	}
	if !o.Fresh() {
		return value + " (stale)"
	}
	return value
}

func onChainBalanceText(status *statusSnapshot) string {
	if status == nil {
		return "unavailable"
	}
	return observationText(status.Balance, formatSats(parseBalance(status.Balance.Value.TotalBalance))+" sats")
}

func channelAmountText(status *statusSnapshot, sats int64) string {
	if status == nil {
		return "unavailable"
	}
	return observationText(status.Channels, formatSats(sats)+" sats")
}

func bitcoinSize(o app.Observation[bitcoin.BlockchainInfo]) string {
	return observationText(o, bitcoin.FormatSize(o.Value.SizeOnDisk))
}

func nodeVersion(status *statusSnapshot) string {
	fields := strings.Fields(status.Node.Value.Version)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func statusNotice(status *statusSnapshot) string {
	var parts []string
	for _, source := range []struct {
		name         string
		known, fresh bool
	}{
		{"Node", status.Node.Known(), status.Node.Fresh()},
		{"Balance", status.Balance.Known(), status.Balance.Fresh()},
		{"Channels", status.Channels.Known(), status.Channels.Fresh()},
	} {
		if !source.known {
			parts = append(parts, source.name+" unavailable")
		} else if !source.fresh {
			parts = append(parts, source.name+" stale")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + theme.Warn.Render(strings.Join(parts, "; "))
}
