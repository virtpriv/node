package host

import (
	"errors"
	"fmt"
	"os/user"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

const (
	bitcoinUser   = "bitcoin"
	lndUser       = "lnd"
	syncthingUser = "syncthing"
	backupGroup   = "vpn-lnd-backup"
)

// CreateSystemUser creates a non-login service identity when it is absent.
// Existing identities are left unchanged; callers own lifecycle admission.
func CreateSystemUser(username, home string) error {
	if _, err := user.Lookup(username); err == nil {
		return nil
	} else {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return fmt.Errorf("look up system user %s: %w", username, err)
		}
	}
	return system.RunRoot("adduser",
		"--system", "--group",
		"--home", home,
		"--shell", "/usr/sbin/nologin",
		username)
}

func createSystemGroup(name string) error {
	if _, err := user.LookupGroup(name); err == nil {
		return nil
	} else {
		var unknown user.UnknownGroupError
		if !errors.As(err, &unknown) {
			return fmt.Errorf("look up system group %s: %w", name, err)
		}
	}
	return system.RunRoot("groupadd", "--system", name)
}

// CreateBaseDaemonIdentities provisions the two base daemon identities and
// directories. Installer must admit the fresh or interrupted lifecycle first;
// this operation does not adopt existing node state.
func CreateBaseDaemonIdentities() error {
	if err := CreateSystemUser(bitcoinUser, paths.BitcoinDataDir); err != nil {
		return err
	}
	if err := CreateSystemUser(lndUser, paths.LNDDataDir); err != nil {
		return err
	}
	if err := createBitcoinDirs(bitcoinUser); err != nil {
		return err
	}
	return createLNDDirs(lndUser)
}
