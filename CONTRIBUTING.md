# Contributing to Virtual Private Node

Thanks for your interest in contributing. This project exists to make running a self-hosted Bitcoin and Lightning node as frictionless as possible, and contributions from the community are welcome.

## Code of conduct

This project follows the [Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/). Be respectful, assume good faith, and focus discussions on what's best for the project and its users.

## Reporting security issues

**Do not file security issues as public GitHub issues.** If you discover a vulnerability that could compromise user funds, private keys, or node integrity, please disclose it privately first. See [SECURITY.md](SECURITY.md) for the full security disclosure policy.

For non-security bugs, open a GitHub issue with steps to reproduce, expected vs actual behavior, and your environment.

## Ways to contribute

### Good first issues

Issues tagged [`good first issue`](https://github.com/virtpriv/node/labels/good%20first%20issue) are scoped to be approachable without deep knowledge of the codebase. Typical examples: small UI tweaks, documentation improvements, accessibility polish.

Issues tagged [`help wanted`](https://github.com/virtpriv/node/labels/help%20wanted) are good for contributors who want to tackle something meatier.

### Translations

The TUI is currently English-only. If you'd like to help translate it into another language, open an issue to discuss. Translation work is especially welcome.

### Documentation

Documentation improvements are always welcome. This includes fixing typos, clarifying confusing sections, adding examples, or translating docs. The `README.md`, `docs/syncthing.md`, `docs/reproducing.md`, and `docs/verifying.md` are all fair game.

### Bug reports and feature requests

Open an issue with as much context as you can. Screenshots help for TUI bugs. For feature requests, describe the user problem first, then your proposed solution.

## Development and validation

The exact Go version declared in `go.mod` is authoritative. Install the
official toolchain from [go.dev/dl](https://go.dev/dl/); distribution
packages may contain patches or different defaults.

Run the portable development gate without elevated privileges:

```bash
gofmt -l $(git ls-files '*.go')
go mod tidy -diff
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./...
git diff --check
```

Tests whose names begin with `TestRoot` exercise real Linux ownership
and filesystem behavior. Outside Linux or without effective UID 0, they
skip with an explicit reason.

Run the privileged gate only on a disposable Debian 13 amd64 system:

```bash
sudo env VPN_REQUIRE_ROOT_TESTS=1 "$(command -v go)" test \
  -count=1 -v ./internal/installer -run '^TestRoot'
```

`VPN_REQUIRE_ROOT_TESTS=1` does not grant privileges. It makes the
selected tests fail instead of skip when their required environment is
unavailable.

Passing the portable and privileged gates does not certify the complete
installer. Changes affecting installation, services, lifecycle state,
permissions, or other host-level behavior also require validation on a
fresh disposable Debian 13 amd64 system. Never perform that validation
on a funded node or an everyday development machine.

## Pull requests

- Open pull requests against `main`.
- Keep each pull request to one coherent change.
- Explain the motivation, approach, and important trade-offs.
- Run the relevant validation gates and report the results.
- Update directly coupled documentation when behavior changes.

### Commit and branch conventions

The project uses Conventional Commits for commit and pull request
titles:

```text
<type>: <description>
```

Common types include `feat`, `fix`, `refactor`, `docs`, `test`,
`build`, `chore`, `perf`, `release`, and `revert`.

Pull requests are normally squash-merged, so the pull request title
becomes the resulting commit title and GitHub appends the pull request
number.

Keep titles under 72 characters when practical. Add a body after a
blank line when the motivation, approach, or trade-offs need more
explanation.

Use the corresponding short branch prefix, such as `feat/`, `fix/`,
`refactor/`, or `docs/`.

Examples:

```text
feat: add immutable bitcoin network profiles
fix: verify auto-unlock transitions
refactor: isolate service identities
docs: document conventional commit policy
```

## Code style

- Format Go code with `gofmt`.
- Prefer explicit behavior over clever abstractions.
- Document non-obvious decisions, security boundaries, and invariants.
- Keep TUI text reasonably narrow for supported terminal layouts.

Thanks for helping build a more sovereign Bitcoin ecosystem.