package host

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A question that hangs answers "cannot tell" at its limit, in words that
// name the program.
func TestAskToolStopsAtItsLimit(t *testing.T) {
	start := time.Now()
	_, err := askTool(time.Second, "sleep", "10")
	if err == nil {
		t.Fatal("a hung question passed")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("did not stop at its limit")
	}
}

// ss's lines as Debian 13.8 prints them for UDP alone, with no protocol
// column (a booted VM), and the forms no image showed.
func TestOnlySSHAnswers(t *testing.T) {
	plain := `UNCONN 0      0               0.0.0.0:5355 0.0.0.0:* users:(("systemd-resolve",pid=388,fd=11))
UNCONN 0      0            127.0.0.54:53   0.0.0.0:* users:(("systemd-resolve",pid=388,fd=20))
UNCONN 0      0         127.0.0.53%lo:53   0.0.0.0:* users:(("systemd-resolve",pid=388,fd=18))
UNCONN 0      0      10.0.2.15%enp0s2:68   0.0.0.0:* users:(("systemd-network",pid=693,fd=30))
UNCONN 0      0                  [::]:5355    [::]:* users:(("systemd-resolve",pid=388,fd=13))
`
	if err := listenersVerdict(plain); err != nil {
		t.Fatalf("refused Debian's listeners: %v", err)
	}
	for _, tc := range []struct {
		line   string
		refuse bool
	}{
		{`UNCONN 0 0 0.0.0.0:5353 0.0.0.0:* users:(("avahi-daemon",pid=1,fd=12))`, true},
		{`UNCONN 0 0 [::]:5353 [::]:* users:(("avahi-daemon",pid=1,fd=13))`, true},
		{`UNCONN 0 0 *:1900 *:*`, true},
		{`UNCONN 0 0 192.168.1.5%eth0:1900 0.0.0.0:* users:(("minissdpd",pid=1,fd=3))`, true},
		{`UNCONN 0 0 127.0.0.1:5353 0.0.0.0:* users:(("x",pid=1,fd=3))`, false},
		{`UNCONN 0 0 [::1]:1900 [::]:* users:(("x",pid=1,fd=3))`, false},
	} {
		err := listenersVerdict(plain + tc.line + "\n")
		if (err != nil) != tc.refuse {
			t.Errorf("%s: got %v", tc.line, err)
		}
	}
}

// The firewall's text compared by rule, its counters and time left out.
func TestFirewallRules(t *testing.T) {
	saved := "*filter\n:INPUT DROP [0:0]\n:ufw-user-input - [0:0]\n-A ufw-user-input -p tcp -m tcp --dport 22 -j ACCEPT\nCOMMIT\n"
	if err := firewallVerdict(saved, saved); err != nil {
		t.Fatal(err)
	}
	added := strings.Replace(saved, "COMMIT", "-A ufw-user-input -p tcp -m tcp --dport 8080 -j ACCEPT\nCOMMIT", 1)
	if err := firewallVerdict(saved, added); err == nil || !strings.Contains(err.Error(), "--dport 8080") {
		t.Fatalf("added rule: %v", err)
	}
	gone := strings.Replace(saved, "-A ufw-user-input -p tcp -m tcp --dport 22 -j ACCEPT\n", "", 1)
	if err := firewallVerdict(saved, gone); err == nil || !strings.Contains(err.Error(), "--dport 22") {
		t.Fatalf("removed rule: %v", err)
	}
}

// fail2ban's server answers as Debian 13.8's did for vpn's jail (a booted
// VM), and with the jail turned off by another file.
func TestFail2banJail(t *testing.T) {
	answers := map[string]string{
		"status":                        "Status\n|- Number of jail:\t1\n`- Jail list:\tsshd",
		"get sshd maxretry":             "5",
		"get sshd findtime":             "600",
		"get sshd bantime":              "600",
		"get sshd actions":              "The jail sshd has the following actions:\nnftables",
		"get sshd action nftables port": "22",
	}
	defer func(old func(...string) (string, error)) { askFail2ban = old }(askFail2ban)
	for _, tc := range []struct {
		name    string
		changed map[string]string
		fail    error
		want    string
	}{
		{"vpn's jail", nil, nil, ""},
		{"another maxretry", map[string]string{"get sshd maxretry": "50"}, nil, "maxretry 50"},
		{"another port", map[string]string{"get sshd action nftables port": "2222"}, nil, "port 2222"},
		{"another action", map[string]string{"get sshd actions": "The jail sshd has the following actions:\niptables"}, nil, "iptables"},
		{"no jail", map[string]string{"status": "Status\n|- Number of jail:\t0\n`- Jail list:\t"}, nil, "without its sshd jail"},
		{"no answer", nil, errors.New("fail2ban-client did not answer within 30 seconds"), "cannot read"},
	} {
		askFail2ban = func(args ...string) (string, error) {
			if tc.fail != nil {
				return "", tc.fail
			}
			key := strings.Join(args, " ")
			if v, ok := tc.changed[key]; ok {
				return v, nil
			}
			return answers[key], nil
		}
		got := fail2banVerdict([]int{22})
		if (got == nil) != (tc.want == "") || (got != nil && !strings.Contains(got.Error(), tc.want)) {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}
