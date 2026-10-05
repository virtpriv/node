package tui

import (
	"fmt"
	"io"
	"os/exec"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/paths"
)

// walletTerminal implements Bubble Tea's interactive command interface without
// capturing passwords or seeds. Process outcomes survive a later failure to
// restore the TUI terminal. Only successful acknowledgement permits continuation.
type walletTerminal struct {
	create, acknowledge *exec.Cmd
	stdin               io.Reader
	stdout, stderr      io.Writer
	execution           app.WalletExecution
}

func newWalletTerminal(network string) *walletTerminal {
	return &walletTerminal{
		create: exec.Command("/usr/local/bin/lncli",
			"--rpcserver="+paths.LNDGRPCEndpoint,
			"--tlscertpath="+paths.StateLNDTLSCert,
			"--network="+network, "create"),
		acknowledge: exec.Command("bash", "-c", walletSeedAcknowledgement),
	}
}

func (t *walletTerminal) SetStdin(r io.Reader)  { t.stdin = r }
func (t *walletTerminal) SetStdout(w io.Writer) { t.stdout = w }
func (t *walletTerminal) SetStderr(w io.Writer) { t.stderr = w }

func (t *walletTerminal) Run() (err error) {
	defer func() { t.execution.Err = err }()
	if _, err := fmt.Fprint(t.stdout, walletCreationIntroduction); err != nil {
		return fmt.Errorf("display wallet instructions: %w", err)
	}
	t.create.Stdin, t.create.Stdout, t.create.Stderr = t.stdin, t.stdout, t.stderr
	if err := t.create.Start(); err != nil {
		return fmt.Errorf("start lncli: %w", err)
	}
	if err := t.create.Wait(); err != nil {
		return fmt.Errorf("lncli ended without confirmed success: %w", err)
	}
	t.execution.Created = true
	t.acknowledge.Stdin, t.acknowledge.Stdout, t.acknowledge.Stderr = t.stdin, t.stdout, t.stderr
	if err := t.acknowledge.Run(); err != nil {
		return fmt.Errorf("wallet created, but seed acknowledgement did not finish: %w", err)
	}
	t.execution.SeedAcknowledged = true
	return nil
}

const walletCreationIntroduction = `
  Lightning Wallet Creation

  Make sure nobody is looking over your shoulder.
  Password entry is invisible; typing and pasting still work.
  Choose and confirm a wallet password, then press 'n' for a new seed.
  The cipher seed passphrase is optional. Keep it with your recovery records
  if you choose one.
  Write down the 24-word seed when LND displays it.

`

// SIGINT is ignored only after lncli succeeds. EOF or terminal loss is failure,
// never acknowledgement, and does not loop forever or imply wallet rollback.
const walletSeedAcknowledgement = `trap '' INT
echo
echo "  Your wallet was created. Preserve the 24-word seed displayed above."
echo "  Store it securely offline, away from this server."
while true; do
  printf "  Type I SAVED MY SEED: "
  IFS= read -r line || exit 1
  [ "$line" = "I SAVED MY SEED" ] && break
  echo "  Please type exactly: I SAVED MY SEED"
done
printf '\033[2J\033[3J\033[H'
`
