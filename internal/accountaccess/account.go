// Package accountaccess defines local account observations shared across IPC.
package accountaccess

import (
	"errors"
	"path/filepath"
	"regexp"

	"github.com/virtpriv/node/internal/sshkeys"
)

type Ref struct {
	Name string `json:"name"`
	UID  uint32 `json:"uid"`
}

var accountName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,31}\$?$`)

func (r Ref) Validate() error {
	if !accountName.MatchString(r.Name) {
		return errors.New("unsupported local account name")
	}
	return nil
}

type Account struct {
	Name  string `json:"name"`
	UID   uint32 `json:"uid"`
	GID   uint32 `json:"gid"`
	Home  string `json:"home"`
	Shell string `json:"shell"`
}

func (a Account) Ref() Ref { return Ref{Name: a.Name, UID: a.UID} }

// KeyDiscoverySupported bounds discovery to root, the owner and accounts
// using common interactive shells. Shell metadata describes configuration, not login proof.
func (a Account) KeyDiscoverySupported() bool {
	switch a.Name {
	case "root", "vpn":
		return true
	case "bitcoin", "bitcoind", "lnd", "syncthing", "debian-tor", "nobody":
		return false
	}
	switch filepath.Base(a.Shell) {
	case "sh", "bash", "dash", "zsh", "fish", "ksh", "csh", "tcsh":
		return true
	}
	return false
}

type SystemAccess string

const (
	AccessRoot         SystemAccess = "root"
	AccessPassword     SystemAccess = "password"
	AccessPasswordless SystemAccess = "passwordless"
	AccessReview       SystemAccess = "review"
	AccessUnavailable  SystemAccess = "unavailable"
)

type Inventory struct {
	Accounts []Account               `json:"accounts"`
	Access   map[string]SystemAccess `json:"access"`
}

type Detail struct {
	Account       Account        `json:"account"`
	Groups        []string       `json:"groups"`
	GroupsProblem string         `json:"groups_problem,omitempty"`
	Source        sshkeys.Source `json:"source"`
}
