# Security Policy

Virtual Private Node (rlvpn) is a Bitcoin and Lightning node. Vulnerabilities in this project can expose users to fund loss, unauthorized node access, or privacy compromise. Security reports are taken seriously and handled privately. *This is not where you store your net worth in Bitcoin. It's where you spend Bitcoin from, more like a checking account than a savings account.*

## Reporting a vulnerability

**Do not file security issues as public GitHub issues.** Public disclosure before a fix is available puts every running node at risk.

Report vulnerabilities privately by email to:

```
mail@ripsline.com
```

If your report contains sensitive details, we will discuss an encrypted path to communicate.
If you send from a Proton email, the message will be encrypted.

Please include as much of the following as you can:

- A description of the vulnerability and its potential impact
- Steps to reproduce, or a proof-of-concept if available
- The affected version(s) of rlvpn
- Your environment (Debian version, Bitcoin Core / LND versions if relevant)
- Whether you plan to disclose publicly, and on what timeline

## What to expect

- **Acknowledgment within 7 days.** This is a samll team, so response time may be slower than a larger team, but every report will be read and acknowledged.
- **Triage and assessment.** Once acknowledged, the report will be investigated and its severity assessed. You will be kept informed of progress.
- **Coordinated disclosure.** If the vulnerability is confirmed, a fix will be developed privately. Once a release containing the fix is published, the vulnerability will be disclosed publicly, and your contribution will be credited unless you prefer to remain anonymous.
- **Best-effort resolution.** As a small team, there is no fixed SLA for fix timelines. Critical issues affecting user funds will be prioritized over everything else.

## Scope

The following are in scope for security reports:

- Vulnerabilities that could compromise user funds or private keys
- Vulnerabilities that could compromise node integrity (remote code execution, privilege escalation, unauthorized access)
- Weaknesses in the installer, update mechanism, or reproducible build process
- Weaknesses in the Tor routing, SSH hardening, or firewall configuration applied by rlvpn
- Supply chain concerns affecting the release pipeline (signing, checksums, distribution)

The following are **out of scope**:

- Bugs in upstream Bitcoin Core, LND, Tor, or Syncthing themselves — please report those to their respective projects
- Cosmetic TUI bugs with no security impact
- Issues requiring physical access to an already-compromised machine
- Denial of service against a user's own node from the user's own network
- Social engineering attacks against the maintainer or users

## Safe harbor

Good-faith security research is welcomed. If you follow this policy when reporting a vulnerability — reporting privately, giving reasonable time for a fix, and not exploiting the issue beyond what is necessary to demonstrate it — no legal action will be pursued against you for the research itself.

## Acknowledgments

Security researchers who responsibly disclose vulnerabilities will be credited in the release notes for the fix, unless they request anonymity.