# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| v0.x    | Yes (pre-release, best-effort) |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please report vulnerabilities via one of:

1. **GitHub Security Advisories:** [Report a vulnerability](https://github.com/Prismer-AI/k8s4claw/security/advisories/new)
2. **Email:** security@prismer.ai

### What to Include

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### Response Timeline

- **Acknowledgment:** within 48 hours
- **Initial assessment:** within 1 week
- **Fix or mitigation:** coordinated disclosure within 90 days

## Security Measures

This project employs:

- **CodeQL** — Static analysis on every PR and weekly scans
- **golangci-lint** — Includes security-focused linters (gosec, gocritic)
- **Dependabot + Renovate** — Automated dependency updates for CVE patches
- **Cosign** — Keyless signing of container images via Sigstore
- **Distroless images** — Minimal attack surface for operator container
- **RBAC least privilege** — Operator and Claw Pod ServiceAccounts follow least privilege
- **GitHub Action SHA pinning** — All CI actions pinned to full commit SHA
