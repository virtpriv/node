package lndrpc

import (
	"context"
	"fmt"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/virtpriv/node/internal/paths"
	"google.golang.org/grpc"
)

// ReadWalletSetupState uses the unauthenticated State service. Wallet creation
// cannot require a macaroon that only exists after the wallet is initialized.
// It neither probes GetInfo nor requests credential repair.
func ReadWalletSetupState(parent context.Context) (WalletState, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return WalletStateUnknown, err
	}
	creds, err := stagedTLSCredentials()
	if err != nil {
		return WalletStateUnknown, err
	}
	conn, err := grpc.NewClient(paths.LNDGRPCEndpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return WalletStateUnknown, err
	}
	defer conn.Close()
	return readWalletSetupState(ctx, lnrpc.NewStateClient(conn))
}

func readWalletSetupState(ctx context.Context, client lnrpc.StateClient) (WalletState, error) {
	resp, err := client.GetState(ctx, &lnrpc.GetStateRequest{})
	if err != nil {
		return WalletStateUnknown, err
	}
	if resp == nil {
		return WalletStateUnknown, fmt.Errorf("LND returned no wallet state")
	}
	return walletStateName(resp.State), nil
}
