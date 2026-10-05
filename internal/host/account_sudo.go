package host

import (
	"strings"

	"github.com/virtpriv/node/internal/accountaccess"
)

// Summarize only simple, uniform grants from sudo's C-locale long listing.
// This is an observation, not an authorization check or a sudoers interpreter.
// Restrictions, mixed authentication and unfamiliar settings need review.
func summarizeAccountSudo(listing, name string) accountaccess.SystemAccess {
	parts := strings.Split(listing, "Sudoers entry:")
	if len(parts) < 2 {
		return accountaccess.AccessReview
	}
	heading := "Matching Defaults entries for " + name + " on "
	before, defaults, ok := strings.Cut(strings.TrimSpace(parts[0]), "\n")
	if !ok || !strings.HasPrefix(before, heading) || !strings.HasSuffix(before, ":") {
		return accountaccess.AccessReview
	}
	defaults, trailer, ok := strings.Cut(defaults, "User "+name+" may run the following commands on ")
	if !ok || strings.Count(strings.TrimSpace(trailer), "\n") != 0 || !strings.HasSuffix(strings.TrimSpace(trailer), ":") {
		return accountaccess.AccessReview
	}
	authenticate, ok := simpleSudoOptions(defaults, true, true)
	if !ok {
		return accountaccess.AccessReview
	}
	result := accountaccess.SystemAccess("")
	for _, entry := range parts[1:] {
		lines := strings.Split(strings.TrimSpace(entry), "\n")
		if len(lines) < 4 || strings.TrimSpace(lines[0]) == "" {
			return accountaccess.AccessReview
		}
		users, groups, options, commands := false, false, false, false
		auth := authenticate
		count := 0
		for _, raw := range lines[1:] {
			line := strings.TrimSpace(raw)
			if line == "" {
				continue
			}
			if commands {
				if line != "ALL" {
					return accountaccess.AccessReview
				}
				count++
				continue
			}
			switch {
			case line == "RunAsUsers: ALL" && !users:
				users = true
			case line == "RunAsGroups: ALL" && !groups:
				groups = true
			case strings.HasPrefix(line, "Options:") && !options:
				options = true
				auth, ok = simpleSudoOptions(strings.TrimPrefix(line, "Options:"), auth, false)
				if !ok {
					return accountaccess.AccessReview
				}
			case line == "Commands:":
				commands = true
			default:
				return accountaccess.AccessReview
			}
		}
		if !users || count != 1 {
			return accountaccess.AccessReview
		}
		current := accountaccess.AccessPasswordless
		if auth {
			current = accountaccess.AccessPassword
		}
		if result != "" && current != result {
			return accountaccess.AccessReview
		}
		result = current
	}
	return result
}

func simpleSudoOptions(text string, authenticate, defaults bool) (bool, bool) {
	seenAuth := false
	for _, raw := range strings.Split(strings.TrimSpace(text), ",") {
		option := strings.TrimSpace(raw)
		switch option {
		case "":
		case "authenticate", "!authenticate":
			if seenAuth {
				return false, false
			}
			seenAuth = true
			authenticate = option == "authenticate"
		case "!rootpw", "!targetpw", "!runaspw", "!exempt_group":
			if !defaults {
				return false, false
			}
		case "env_reset", "mail_badpass", "use_pty":
			if !defaults {
				return false, false
			}
		default:
			// A simple secure_path does not alter authorization or authentication.
			if !defaults || !strings.HasPrefix(option, "secure_path=") || strings.ContainsAny(option, "\"' \t\n") {
				return false, false
			}
		}
	}
	return authenticate, true
}
