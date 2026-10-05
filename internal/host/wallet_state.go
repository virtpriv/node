package host

import (
	"errors"
	"fmt"

	"github.com/lightningnetwork/lnd/lnrpc"

	"github.com/virtpriv/node/internal/config"
)

// WalletExists asks LND's always-running State service whether its wallet has
// been initialized. SQLite creates chain.sqlite before a wallet exists, so no
// database-file path can answer this question reliably.
func WalletExists(network string) (bool, error) {
	_, err := config.NetworkConfigFromName(network)
	if err != nil {
		return false, err
	}

	state, stateErr := ReadLNDWalletState()
	return walletExistsFromState(state, stateErr)
}

func walletExistsFromState(
	state lnrpc.WalletState, stateErr error,
) (bool, error) {
	if stateErr != nil {
		return false, fmt.Errorf("read LND wallet state: %w", stateErr)
	}

	switch state {
	case lnrpc.WalletState_NON_EXISTING:
		return false, nil

	case lnrpc.WalletState_LOCKED,
		lnrpc.WalletState_UNLOCKED,
		lnrpc.WalletState_RPC_ACTIVE,
		lnrpc.WalletState_SERVER_ACTIVE:

		return true, nil

	case lnrpc.WalletState_WAITING_TO_START:
		return false, errors.New("LND is waiting to start; wallet state is unknown")

	default:
		return false, fmt.Errorf("unknown LND wallet state %d", state)
	}
}
