package host

import (
	"strings"
	"testing"

	"github.com/virtualprivatenode/vpn/internal/accountaccess"
)

func TestAccountSudoSummaryDoesNotOverstatePolicy(t *testing.T) {
	passwordless := strings.Replace(ownerSudoListing, "Options: authenticate", "Options: !authenticate", 1)
	defaultPasswordless := strings.Replace(ownerSudoListing, "    Options: authenticate\n", "", 1)
	defaultPasswordless = strings.Replace(defaultPasswordless, ", authenticate,", ", !authenticate,", 1)
	entry := strings.Split(passwordless, "Sudoers entry:")[1]
	for _, tc := range []struct {
		name, listing string
		want          accountaccess.SystemAccess
	}{
		{"owner password", ownerSudoListing, accountaccess.AccessPassword},
		{"provider passwordless", strings.ReplaceAll(passwordless, "vpn", "test"), accountaccess.AccessPasswordless},
		{"default authentication", strings.Replace(ownerSudoListing, "    Options: authenticate\n", "", 1), accountaccess.AccessPassword},
		{"default passwordless", defaultPasswordless, accountaccess.AccessPasswordless},
		{"mixed grants", ownerSudoListing + "Sudoers entry:" + entry, accountaccess.AccessReview},
		{"partial commands", strings.Replace(ownerSudoListing, "        ALL", "        /usr/bin/true", 1), accountaccess.AccessReview},
		{"negated command", ownerSudoListing + "Sudoers entry: /etc/sudoers\n RunAsUsers: ALL\n Commands:\n !/usr/bin/passwd\n", accountaccess.AccessReview},
		{"limited runas", strings.Replace(ownerSudoListing, "RunAsUsers: ALL", "RunAsUsers: backup", 1), accountaccess.AccessReview},
		{"root password", strings.Replace(ownerSudoListing, "!rootpw", "rootpw", 1), accountaccess.AccessReview},
		{"exempt group", strings.Replace(ownerSudoListing, "!exempt_group", "exempt_group=users", 1), accountaccess.AccessReview},
		{"command defaults", "Runas and Command-specific defaults for vpn:\n !authenticate\n" + ownerSudoListing, accountaccess.AccessReview},
		{"unknown option", strings.Replace(ownerSudoListing, "Options: authenticate", "Options: authenticate, noexec", 1), accountaccess.AccessReview},
		{"warning", "sudo: unable to resolve host node\n" + ownerSudoListing, accountaccess.AccessReview},
		{"no rule", "User vpn is not allowed to run sudo on node.", accountaccess.AccessReview},
		{"truncated", strings.TrimSuffix(ownerSudoListing, "        ALL\n"), accountaccess.AccessReview},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "vpn"
			if tc.name == "provider passwordless" {
				name = "test"
			}
			if got := summarizeAccountSudo(tc.listing, name); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
