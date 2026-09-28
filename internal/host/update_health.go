package host

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/virtualprivatenode/vpn/internal/bitcoin"
	"github.com/virtualprivatenode/vpn/internal/config"
	"github.com/virtualprivatenode/vpn/internal/paths"
	"github.com/virtualprivatenode/vpn/internal/syncthing"
	"github.com/virtualprivatenode/vpn/internal/update/protocol"
	"google.golang.org/grpc/metadata"
)

// UpdateHealth separates startup progress and a locked wallet from failure.
// Synchronization is normal runtime progress; it does not trigger a downgrade.
type UpdateHealth struct{ Ready, Unlock bool }

func CheckUpdateHealth(c protocol.Component, version string, cfg *config.AppConfig, walletPresent bool, syncthingID string) (UpdateHealth, error) {
	if err := CheckUpdateProcess(c); err != nil {
		return UpdateHealth{}, nil
	}
	profile, err := cfg.NetworkConfig()
	if err != nil {
		return UpdateHealth{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch c {
	case protocol.Bitcoin:
		identity, err := bitcoin.GetBlockchainIdentity(profile.RPCPort)
		if err != nil {
			return UpdateHealth{}, nil
		}
		if identity.Chain != profile.CoreNetwork || identity.Genesis != profile.ExpectedGenesis || identity.SignetChallenge != profile.ExpectedSignetChallenge {
			return UpdateHealth{}, errors.New("bitcoin core network identity changed")
		}
		v, err := bitcoin.GetSubversion(profile.RPCPort)
		if err != nil {
			return UpdateHealth{}, nil
		}
		if v != "/Satoshi:"+version+"/" && v != "/Satoshi:"+version+".0/" {
			return UpdateHealth{}, errors.New("bitcoin core RPC version differs from release")
		}
	case protocol.LND:
		state, err := ReadLNDWalletState()
		if err != nil {
			return UpdateHealth{}, nil
		}
		if state == lnrpc.WalletState_LOCKED {
			return UpdateHealth{Unlock: true}, nil
		}
		if state == lnrpc.WalletState_NON_EXISTING {
			if walletPresent {
				return UpdateHealth{}, errors.New("LND no longer reports the existing wallet")
			}
			return UpdateHealth{Ready: true}, nil
		}
		if state != lnrpc.WalletState_RPC_ACTIVE && state != lnrpc.WalletState_SERVER_ACTIVE {
			return UpdateHealth{}, nil
		}
		conn, err := directLNDConn()
		if err != nil {
			return UpdateHealth{}, nil
		}
		defer conn.Close()
		mac, err := os.ReadFile(paths.LNDMacaroon(profile.LNDNetwork))
		if err != nil {
			return UpdateHealth{}, err
		}
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("macaroon", hex.EncodeToString(mac)))
		info, err := lnrpc.NewLightningClient(conn).GetInfo(ctx, &lnrpc.GetInfoRequest{})
		if err != nil {
			return UpdateHealth{}, nil
		}
		// Current LND is Bitcoin-only; its old Chain field is deprecated.
		if len(info.Chains) != 1 || info.Chains[0] == nil || info.Chains[0].Network != profile.LNDNetwork {
			return UpdateHealth{}, errors.New("LND network identity changed")
		}
		if v := strings.Fields(info.Version); len(v) == 0 || strings.TrimPrefix(v[0], "v") != version {
			return UpdateHealth{}, errors.New("LND RPC version differs from release")
		}
	case protocol.Syncthing:
		key, err := SyncthingAPIKey()
		if err != nil {
			return UpdateHealth{}, err
		}
		client := syncthing.NewClient(key)
		raw, err := client.Request(ctx, http.MethodGet, "/rest/system/version", "")
		if err != nil {
			return UpdateHealth{}, nil
		}
		var reply struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal([]byte(raw), &reply); err != nil {
			return UpdateHealth{}, err
		}
		if strings.TrimPrefix(reply.Version, "v") != version {
			return UpdateHealth{}, errors.New("syncthing API version differs from release")
		}
		id, err := client.LocalID(ctx)
		if err != nil {
			return UpdateHealth{}, nil
		}
		if id != syncthingID || id == "" {
			return UpdateHealth{}, errors.New("syncthing device identity changed")
		}
		if err := client.ConfirmPrivacy(ctx); err != nil {
			return UpdateHealth{}, fmt.Errorf("syncthing privacy check: %w", err)
		}
	default:
		return UpdateHealth{}, errors.New("unknown component")
	}
	return UpdateHealth{Ready: true}, nil
}
