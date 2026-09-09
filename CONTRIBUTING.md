# Contributing

Contributions are welcome through GitHub issues and pull requests.

## Development setup

Use Linux or macOS with Go 1.26.0 or newer, system OpenSSH, and Git. The Go module uses only the standard library; no dependency installation or language runtime is required for the built binary.

```bash
go build -o dist/vpsctl .
./dist/vpsctl --version
```

## Validation

```bash
go test -race ./...
go vet ./...
go build -o dist/vpsctl .
```

Default tests must not depend on a real VPS. Test process boundaries with local fixtures; Linux-specific shell tests are skipped on macOS. Keep JSON result, unknown-outcome handling, quoting, host verification, and safe file replacement covered when changing behavior.

For the opt-in real OpenSSH integration test, use an **isolated Linux environment with root privileges**, OpenSSH server/client, rsync 3+, GNU coreutils, and util-linux installed:

```bash
sudo mkdir -p /run/sshd
sudo env "PATH=$PATH:/usr/sbin" VPSCTL_INTEGRATION=1 go test -run '^TestSSHIntegration$' -v .
```

The test creates temporary client/host keys, a verified `known_hosts`, an sshd configuration, and a forced temporary remote HOME. It runs a local test sshd, not a real VPS. Do not run it against production or change your real SSH configuration for it. Without opt-in or Linux root privileges it is skipped.

When changing `skills/vpsctl/`, install the `skills` CLI and run:

```bash
scripts/check-skill-package.sh
```

This validates Skill discovery and isolated Universal/Pi installation; it does not install or release the CLI binary.

Pushing an exact `vX.Y.Z` tag runs CI, including the isolated Linux OpenSSH integration test, then publishes checksum-verified GitHub Release binaries. The tag must not already have a release. Do not bundle Skills into that release.

## Pull requests

- Keep changes focused; prefer the standard library and system OpenSSH over additional dependencies.
- Add regression coverage for behavior changes and bug fixes.
- Update `README.md`, the Skill reference, and `CHANGELOG.md` when user-facing behavior changes.
- Preserve Agent-only JSON, noninteractive key authentication, strict pre-provisioned host verification, and no blind retries after unknown outcomes.
- Never commit private keys, passwords, tokens, host inventories, or real production configuration.
- Use clear commit messages that state the behavior changed and why.

By contributing, you agree that your contribution is licensed under the MIT License.
