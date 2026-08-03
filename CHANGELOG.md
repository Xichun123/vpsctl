# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Agent Skill package compatible with the Agent Skills specification and the `skills` CLI.
- GitHub Actions checks for Python 3.9 and 3.13, package builds, and Skill installation.
- Standard GitHub community health files and contribution guidance.
- Regression tests for persistent SSH host-key verification.

### Changed

- Command result JSON omits `stderr` when it has no content; non-empty warnings and errors remain unchanged.
- SSH, SCP, Paramiko, jump-host, daemon, and server-transfer connections now use persistent `known_hosts` verification with accept-new behavior.

### Fixed

- Python 3.9 compatibility for streamed stderr formatting and interleaved `vpsctl apply` arguments.

### Removed

- Generated build and egg-info artifacts from the source tree.

## [0.3.0] - 2026-08-02

### Added

- Unified `vpsctl` command for SSH host management, command execution, transfers, tunnels, clusters, and key management.
- Local project profiles, remote state snapshots, compact Agent context, and explicit refresh operations.
- Recorded project mutations through `vpsctl apply` and supplemental change journal entries.

[Unreleased]: https://github.com/Xichun123/vpsctl/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/Xichun123/vpsctl/releases/tag/v0.3.0
