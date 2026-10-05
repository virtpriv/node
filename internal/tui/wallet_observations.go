package tui

import (
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
)

type walletObservationScope struct {
	network      string
	walletExists bool
	generation   uint64
	client       *lndrpc.Client
}

func (c *ScreenContext) walletObservationScope() walletObservationScope {
	scope := walletObservationScope{generation: c.walletGeneration, client: c.LndClient}
	if c.Cfg != nil {
		scope.network = c.Cfg.Network
	}
	if c.State != nil {
		scope.walletExists = c.State.WalletExists
	}
	return scope
}

const previousWalletDetail = "This detail belongs to a previous wallet session. Reopen it from the current list."

func observedListRecord[T any](observation app.Observation[[]T], match func(T) bool) (T, string, bool) {
	if observation.Known() {
		for _, record := range observation.Value {
			if match(record) {
				notice := ""
				if observation.Err != nil {
					notice = "Showing stale data. Retrying..."
				}
				return record, notice, true
			}
		}
	}
	var zero T
	if observation.Err != nil {
		return zero, "List unavailable. Retrying...", false
	}
	if !observation.Known() {
		return zero, "Loading...", false
	}
	return zero, "Not in the latest successful list.", false
}

func observedEmptyText[T any](observation app.Observation[T], empty string) string {
	if observation.Fresh() {
		return empty
	}
	if observation.Err != nil {
		return "Unavailable. Retrying..."
	}
	return "Loading..."
}
