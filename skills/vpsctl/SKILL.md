---
name: vpsctl
description: Operate remote Linux VPS hosts through the Agent-only JSON vpsctl CLI, including SSH host inventory, commands, durable jobs, file transfers, tunnels, jump hosts, and keys. Use for remote VPS administration or deployment tasks; do not use for localhost-only work.
license: MIT
compatibility: Requires vpsctl 0.4.0 on Linux or macOS, system OpenSSH, pre-provisioned verified known_hosts, and noninteractive key or ssh-agent authentication. Remote Linux needs POSIX sh and GNU coreutils; durable jobs also need procfs and util-linux setsid. Recursive or resumable transfers require rsync 3+ at both ends. Remote operations require network access.
metadata:
  author: Xichun123
  version: "0.4.0"
---

# vpsctl

Use the installed `vpsctl` command for remote SSH and VPS operations. Do not depend on this Skill's installation path or invoke package-internal source files.

Read [references/commands.md](references/commands.md) for exact syntax, JSON semantics, and operation limits.

## Preconditions

Run `vpsctl --version` and inspect its JSON `data.version`. This Skill targets the breaking 0.4.0 CLI. If unavailable, ask for the GitHub Release binary or a source build on `PATH`; installing this Skill does not install the CLI.

Before connecting, require the exact target alias, trusted SSH configuration, verified server fingerprints already provisioned in `known_hosts`, and an available key or unlocked `ssh-agent`. There are no password/passphrase prompts or automatic acceptance of host keys. Jump hosts require the same preparation.

## Mandatory policy

- Discover aliases with `vpsctl host list` or `vpsctl host find <query>`. Inventory is syntactic, not proof of connectivity or identity.
- Prefer `vpsctl` for remote commands and transfers. Use `exec` for both read-only commands and authorized writes; no project registration is required.
- Confirm targets, remote absolute paths, and impact before production changes, deletion, overwrites, or key writes. Batch commands require an explicit host list.
- Protect `.env`, private keys, certificates, and production configuration; do not overwrite, delete, or recreate sensitive paths without explicit approval.
- Never expose tokens, passwords, or private keys in commands, scripts, logs, or reports. Detached jobs persist scripts and logs. Base64 and file permissions are not secret storage.
- Stop on host-key rejection; independently verify and provision the correct fingerprint. Never bypass verification or blindly trust `ssh-keyscan` output.
- Treat SSH configuration as trusted executable input: directives such as `ProxyCommand` and `Match exec` may run local code. Do not load untrusted configs.
- Read JSON `status`, `exit_code`, `error`, and `truncated`, not the removed `success` field. `completed` alone is not success; require `exit_code == 0` and `error == null`. `running` acknowledges a job launch only.
- A timeout, cancellation, or SSH disconnect may leave remote work running. On `unknown`, reconcile state or the returned job ID before any further mutation; never blindly retry.
- Combine independent read-only checks for one host when practical. Use `--stdin` or `--script-file` for complex quoting, heredocs, JSON/YAML, or long scripts. Scripts use POSIX `sh`; invoke another interpreter explicitly when needed.
- Put global `--timeout`, `--ssh-config`, and `--max-output` options before the command. A timeout limits local waiting, not remote execution.
- Use the smallest relevant read-only verification after a change. Failed commands can make partial changes; never claim success without validation.

## Workflow

1. Discover and confirm the host, remote paths, authentication, and verified host keys.
2. Inspect only the relevant state with `exec`.
3. Confirm the modification scope and sensitive-path impact.
4. Run the authorized command. Use `--cwd` for its remote working directory and `--detach` for durable long work.
5. For detached work, retain `data.job_id` even after an uncertain launch. Query `job status` and page `job output` using `data.next_cursor` until reconciled; do not submit a duplicate job.
6. Verify the changed behavior and report failures, unknown outcomes, or output truncation explicitly.

```bash
vpsctl exec <alias> '<read-only-command>'
vpsctl exec <alias> --cwd /opt/my-app '<authorized-write-command>'
vpsctl exec <alias> --cwd /opt/my-app --detach --script-file ./deploy.sh
vpsctl job status <alias> <job-id>
vpsctl job output <alias> <job-id> --cursor 0 --limit 65536
```

Job output cursors count bytes, not characters. Decode `data.output_base64` for lossless logs and advance with `data.next_cursor`; `stdout` is a display-only UTF-8 conversion. Logs are uncapped and not rotated; arrange authorized cleanup when no longer needed. Output exhaustion is not task completion.

Single-file transfers default to no replacement and use checksums plus atomic commit. Explicit `--overwrite` requires approval. Recursive/resume uses rsync 3+ at both ends, skips existing files unless overwriting, and is not a directory-wide transaction. Temporary files use `0600`; uploaded files are not automatically executable.
