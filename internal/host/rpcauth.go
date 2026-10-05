package host

// Core stores salted HMACs for two independent RPC identities. The operator's
// password is published on the staging board; LND's password stays in its
// protected configuration. Neither client needs access to Core's data directory.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/virtpriv/node/internal/bitcoin"
)

type nodeRPCAuthCredentials struct {
	lines       []string
	lndPassword string
}

// generateRPCAuth produces an rpcauth= line and the matching
// cleartext password. It reproduces Bitcoin Core's reference
// generator (share/rpcauth/rpcauth.py) exactly:
//
//   - password: 32 random bytes, unpadded URL-safe base64;
//   - salt: 16 random bytes as 32 lowercase hex chars;
//   - HMAC-SHA256 keyed by the hex salt STRING (not the raw
//     bytes; reversing this yields a line that never
//     authenticates), message = the password bytes.
func generateRPCAuth(user string) (line, password string) {
	var pw [32]byte
	rand.Read(pw[:])
	password = base64.RawURLEncoding.EncodeToString(pw[:])

	var salt [16]byte
	rand.Read(salt[:])
	saltHex := hex.EncodeToString(salt[:])

	mac := hmac.New(sha256.New, []byte(saltHex))
	mac.Write([]byte(password))
	line = fmt.Sprintf("rpcauth=%s:%s$%s",
		user, saltHex, hex.EncodeToString(mac.Sum(nil)))
	return line, password
}

// writeRPCAuthCredentials regenerates both local identities.
// The TUI password is staged on the board; the LND password is
// returned only long enough for the caller to write lnd.conf.
func writeRPCAuthCredentials() (nodeRPCAuthCredentials, error) {
	uiLine, uiPassword := generateRPCAuth(bitcoin.RPCUser)
	lndLine, lndPassword := generateRPCAuth(LNDBitcoindRPCUser)
	if err := StageBitcoindRPCPassword(uiPassword); err != nil {
		return nodeRPCAuthCredentials{}, err
	}
	return nodeRPCAuthCredentials{
		lines:       []string{uiLine, lndLine},
		lndPassword: lndPassword,
	}, nil
}
