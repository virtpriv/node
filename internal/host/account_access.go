package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/accountaccess"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/sshkeys"
	"golang.org/x/sys/unix"
)

const (
	accountFileLimit = 128 << 10
	keyFileLimit     = 64 << 10
)

type accountObserver struct {
	root      *os.File
	systemUID uint32
}

func openAccountObserver() (*accountObserver, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("local account observation requires root")
	}
	root, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	return &accountObserver{root: root}, nil
}

func ReadLocalAccounts() (accountaccess.Inventory, error) {
	o, err := openAccountObserver()
	if err != nil {
		return accountaccess.Inventory{}, err
	}
	defer o.root.Close()
	accounts, err := o.accounts()
	if err != nil {
		return accountaccess.Inventory{}, err
	}
	// Bound the whole inventory, including hosts with slow policy lookups.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	access := make(map[string]accountaccess.SystemAccess)
	for _, a := range accounts {
		if !a.KeyDiscoverySupported() {
			continue
		}
		if a.UID == 0 {
			access[a.Name] = accountaccess.AccessRoot
			continue
		}
		listing, err := observeAccountSudo(ctx, a.Name)
		access[a.Name] = accountaccess.AccessUnavailable
		if err == nil {
			access[a.Name] = summarizeAccountSudo(listing, a.Name)
		}
	}
	return accountaccess.Inventory{Accounts: accounts, Access: access}, nil
}

// ReadLocalAccount accepts a local identity, never a caller-selected path or
// command. Each request resolves that identity again from the local database.
func ReadLocalAccount(ref accountaccess.Ref) (accountaccess.Detail, error) {
	if err := ref.Validate(); err != nil {
		return accountaccess.Detail{}, err
	}
	o, err := openAccountObserver()
	if err != nil {
		return accountaccess.Detail{}, err
	}
	defer o.root.Close()
	a, err := o.account(ref)
	if err != nil {
		return accountaccess.Detail{}, err
	}
	detail := accountaccess.Detail{Account: a, Source: o.keys(a)}
	detail.Groups, err = o.groups(a)
	if err != nil {
		detail.GroupsProblem = err.Error()
	}

	return detail, nil
}

func (o *accountObserver) accounts() ([]accountaccess.Account, error) {
	data, err := o.readFile("etc/passwd", o.systemUID, accountFileLimit)
	if err != nil {
		return nil, fmt.Errorf("read local accounts: %w", err)
	}
	accounts := make([]accountaccess.Account, 0)
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 7 {
			return nil, errors.New("local account database contains a malformed entry")
		}
		uid, uidErr := strconv.ParseUint(fields[2], 10, 32)
		gid, gidErr := strconv.ParseUint(fields[3], 10, 32)
		a := accountaccess.Account{Name: fields[0], UID: uint32(uid), GID: uint32(gid), Home: fields[5], Shell: fields[6]}
		if a.Ref().Validate() != nil || uidErr != nil || gidErr != nil || seen[a.Name] ||
			!filepath.IsAbs(a.Home) || filepath.Clean(a.Home) != a.Home || strings.ContainsAny(a.Home+a.Shell, "\x00\r") {
			return nil, errors.New("local account database contains an unsupported or ambiguous identity")
		}
		seen[a.Name] = true
		accounts = append(accounts, a)
		if len(accounts) > 256 {
			return nil, errors.New("local account inventory exceeds 256 entries")
		}
	}
	slices.SortFunc(accounts, func(a, b accountaccess.Account) int {
		if (a.Name == "root") != (b.Name == "root") {
			if a.Name == "root" {
				return -1
			}
			return 1
		}
		if (a.Name == paths.AdminUser) != (b.Name == paths.AdminUser) {
			if a.Name == paths.AdminUser {
				return -1
			}
			return 1
		}
		if a.KeyDiscoverySupported() != b.KeyDiscoverySupported() {
			if a.KeyDiscoverySupported() {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return accounts, nil
}

func (o *accountObserver) account(ref accountaccess.Ref) (accountaccess.Account, error) {
	if err := ref.Validate(); err != nil {
		return accountaccess.Account{}, err
	}
	accounts, err := o.accounts()
	if err != nil {
		return accountaccess.Account{}, err
	}
	for _, a := range accounts {
		if a.Ref() == ref {
			return a, nil
		}
	}
	return accountaccess.Account{}, errors.New("local account changed or disappeared; refresh the account list")
}

func (o *accountObserver) keys(a accountaccess.Account) sshkeys.Source {
	source := sshkeys.Source{User: a.Name, Path: filepath.Join(a.Home, ".ssh", "authorized_keys")}
	if !a.KeyDiscoverySupported() {
		source.Problem = "standard public-key discovery is not offered for this service or non-login account"
		return source
	}
	data, err := o.readFile(strings.TrimPrefix(source.Path, "/"), a.UID, keyFileLimit)
	if errors.Is(err, os.ErrNotExist) {
		return source
	}
	if err != nil {
		source.Problem = err.Error()
		return source
	}
	source.Keys, source.Excluded = sshkeys.Classify(string(data))
	return source
}

func (o *accountObserver) groups(a accountaccess.Account) ([]string, error) {
	data, err := o.readFile("etc/group", o.systemUID, accountFileLimit)
	if err != nil {
		return nil, fmt.Errorf("read local groups: %w", err)
	}
	var groups []string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 4 {
			return nil, errors.New("local group database contains a malformed entry")
		}
		gid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return nil, errors.New("local group database contains an invalid group ID")
		}
		if uint32(gid) == a.GID || slices.Contains(strings.Split(fields[3], ","), a.Name) {
			groups = append(groups, fields[0])
		}
	}
	slices.Sort(groups)
	return slices.Compact(groups), nil
}

// Walk with directory descriptors so renamed directories cannot redirect a
// privileged read. Every component rejects symlinks, foreign ownership and
// group/other writes; a special file cannot block the helper on open.
func (o *accountObserver) readFile(path string, uid uint32, limit int) ([]byte, error) {
	if filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, "../") || (path == "." || path == "..") {
		return nil, errors.New("unsupported observation path")
	}
	fd, owned := int(o.root.Fd()), false
	defer func() {
		if owned {
			unix.Close(fd)
		}
	}()
	parts := strings.Split(path, "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, part, flags, 0)
		if err != nil {
			return nil, fmt.Errorf("cannot safely open public account data: %w", err)
		}
		if owned {
			unix.Close(fd)
		}
		fd, owned = next, true
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return nil, err
		}
		if (stat.Uid != uid && stat.Uid != o.systemUID) || stat.Mode&0022 != 0 {
			return nil, errors.New("public account data has unsafe ownership or write permissions")
		}
		if i == len(parts)-1 && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size > int64(limit)) {
			return nil, errors.New("public account data must be a single regular file within the size limit")
		}
	}
	file := os.NewFile(uintptr(fd), path)
	owned = false // file owns the descriptor from here.
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("public account data exceeds the size limit")
	}
	return data, nil
}

type accountOutput struct{ data []byte }

func (w *accountOutput) Write(p []byte) (int, error) {
	if len(w.data)+len(p) > 32<<10 {
		return 0, errors.New("sudo observation exceeds the size limit")
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func observeAccountSudo(parent context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", "-ll", "-U", name)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = time.Second
	output := &accountOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if err != nil {
		// A nonzero status can mean denial, an invalid policy or a lookup
		// failure. Keep its evidence without claiming the account has no sudo.
		return string(output.data), fmt.Errorf("sudo access was not confirmed: %w", err)
	}
	return string(output.data), nil
}
