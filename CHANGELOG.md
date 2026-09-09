# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Durable remote jobs with `exec --detach`, explicit job IDs, status reconciliation, and byte-cursor output with lossless base64 data.
- Checksum-verified atomic single-file transfers, explicit overwrite control, and rsync 3+ recursive/resumable transfers.
- Agent Skill package compatible with the Agent Skills specification and the `skills` CLI.
- Linux/macOS Go CI, checksum-verified GitHub Release binaries, and an opt-in isolated Linux OpenSSH integration test.
- Separate CLI installer (`scripts/install.sh`) that writes `~/.local/bin/vpsctl` without installing Skills.
- Standard GitHub community health files and contribution guidance.

### Changed

- **Breaking 0.4.0 rewrite:** Go 1.26.0 standard library plus system OpenSSH, supporting local Linux/macOS and remote Linux. Install a Release binary or build with `go build -o dist/vpsctl .`.
- Agent-only JSON for every response: `status`, nullable `exit_code`, `stdout`, `stderr`, `error`, `truncated`, and optional `data`; empty streams are retained and `success` is removed.
- Key/ssh-agent authentication only, no prompts, and strict pre-provisioned verified `known_hosts` rather than accepting unknown keys.
- `exec` handles both reads and authorized writes, with explicit remote `--cwd`. Global duration-based `--timeout` limits local waiting only; uncertain remote outcomes must be reconciled, never blindly retried.
- Host editing uses explicit `--alias` and conservative config handling; cluster execution requires explicit hosts and numeric parallelism.
- Server-to-server transfer streams one file through the local machine. Tunnels use OpenSSH control masters with explicit host/ID operations; key rollback requires a specific backup ID.

### Fixed

- Isolate script input from child stdin so read/cat cannot silently consume later commands; separate OpenSSH client diagnostics from remote stderr before classifying failures.
- Publish key backups atomically so interrupted copies cannot be used for rollback; preserve default OpenSSH configuration behavior for tunnels.
- Match OpenSSH comment and escape parsing when scanning Includes; refuse unsafe host edits without changing existing identity paths.
- Reject directory/nonregular single-file targets and use exact-target atomic commits; keep checksum protocol responses independent of display output limits.
- Add regression checks for all eight audit findings and extend isolated Linux SSH integration coverage.

### Removed

- Python packaging/runtime and third-party runtime libraries; password authentication, vpsctl connection daemon, and automatic reconnect/retry workflows.
- Legacy command flags and response contracts, including host create/export and environment/tag filters, transfer direct/hybrid modes, tunnel stop-all, and key deploy/migrate.
- Project profiles, recorded changes, remote snapshots, local context, and SQLite state storage (`project`, `apply`, `change`, `refresh`, and `context`). Existing local databases are left untouched.
- Legacy SSH configuration migration, annotation, and repair (`config`), plus system inventory collection (`inventory refresh`).

## [0.3.0] - 2026-08-02

### Added

- Unified `vpsctl` command for SSH host management, command execution, transfers, tunnels, clusters, and key management.
- Local project profiles, remote state snapshots, compact Agent context, and explicit refresh operations.
- Recorded project mutations through `vpsctl apply` and supplemental change journal entries.

[Unreleased]: https://github.com/Xichun123/vpsctl/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/Xichun123/vpsctl/releases/tag/v0.3.0
