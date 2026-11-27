# Security Policy

## Supported Versions

Security fixes are applied to the latest `0.1.x` release on `main`.

## Security Model

`quench` runs verification commands in your repository. Key built-in guardrails:

- **Docker hardening:** Warm containers start with `--cap-drop ALL --security-opt no-new-privileges:true`, optional `sandbox.user: nonroot`, optional `memory`/`cpus` limits, and optional post-sync `network: none`.
- **Copy-mode exclusions:** Remote Docker `copy` mode excludes `.env*`, `*.key`, and `*.pem` by default (plus custom `sandbox.copy_exclude`).
- **MCP workspace confinement:** `quench mcp` confines `project_dir` to the startup workspace root unless additional roots are explicitly granted via `--allow-dir` or `QUENCH_MCP_ALLOW_DIRS`.
- **Command safety:** Diff-resolved targets are POSIX single-quote escaped before shell interpolation.

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Instead, report vulnerabilities via [GitHub Security Advisories](https://github.com/akhanatoon/quench/security/advisories/new) with:

- A clear description of the issue and affected version (`quench --version`)
- Reproduction steps or a minimal proof of concept
- Any suggested fix or mitigation

We aim to acknowledge reports within 48 hours and ship a fix promptly once reproduced.
