# vpsctl 0.4.0 command reference

This is the breaking Go/OpenSSH CLI for Agents on Linux and macOS. `vpsctl --help` and `vpsctl --version` return JSON. Do not assume old command flags or response fields still work.

## Global options and results

Global options precede the command:

```bash
vpsctl [--ssh-config FILE] [--timeout DURATION] [--max-output BYTES] COMMAND ...
vpsctl --timeout 30s --max-output 1048576 exec <alias> 'uptime'
```

- `--ssh-config`: alternate trusted OpenSSH configuration; otherwise standard user configuration applies.
- `--timeout`: nonnegative Go duration such as `30s` or `5m`; default `0` means unlimited local waiting. It does not cancel remote work.
- `--max-output`: maximum retained bytes per output stream, default 1048576, range 1..67108864.
- SSH connection timeout is 15 seconds with one connection attempt. Authentication is noninteractive public-key only; unlock encrypted identities in `ssh-agent` beforehand. There is no password fallback or connection daemon.
- Pre-provision independently verified `known_hosts` entries for targets and jump hosts. Unknown or changed host keys fail closed. Do not disable verification or automatically trust scanned keys.

Every result has `status`, `exit_code`, `stdout`, `stderr`, `error`, and `truncated`; command-specific metadata is in optional `data`. Empty output streams remain empty strings. Errors have `code` and `message`.

| Status | Interpretation |
|---|---|
| `completed` | An exit code is available; require `exit_code == 0` and `error == null` for success. |
| `running` | Detached job was acknowledged or is still running; not task completion. |
| `failed` | Validation, dependency, authentication, or operation failure; inspect error and any partial effects. |
| `unknown` | No trustworthy completion outcome; reconcile before any retry. |

`exit_code: null` means no completion exit code is available. The CLI process exits 0 for success or acknowledged running jobs, otherwise 1; it does not mirror remote nonzero codes. Do not use the removed `success` field. A lost SSH connection or local timeout can leave remote work running. Never blindly retry a mutation after an uncertain result.

## Host inventory and editing

```bash
vpsctl host list
vpsctl host find <query>
vpsctl host show <alias>
vpsctl list
vpsctl find <query>
vpsctl host add --alias <alias> --hostname <address> \
  [--user <user>] [--identity <private-key-path>] [--port 22] [--jump <alias>]
vpsctl host update --alias <alias> [--hostname <address>] [--user <user>] \
  [--identity <private-key-path>] [--port 22] [--jump <alias>]
vpsctl host delete --alias <alias>
```

Inventory lists literal aliases and source files, follows static `Include` globs, and does not evaluate `Match`. It is not a connection test. A preceding `# vpsctl-labels: web production` comment supplies searchable labels. There are no environment/tag filters or export command.

`host show` resolves configuration using `ssh -G`, returning its text in `stdout`; it does not authenticate remotely. It refuses configs containing `Match` because `Match exec` can execute local code. SSH config is trusted executable input, not a safe format for untrusted uploads.

Host mutations modify the selected local SSH config, create a backup reported in `data.backup`, and use a lock and atomic `0600` replacement. Editing refuses `Include`, `Match`, duplicate/shared target blocks, and unsupported target directives rather than guessing. Use explicit manual review for complex configurations. Do not remove a stale lock until confirming no writer is active.

## Command execution

Use `exec` for reads and authorized writes. Confirm the exact host, sensitive paths, and impact before writes; no registration is required.

```bash
vpsctl exec <alias> '<command>'
vpsctl exec <alias> --cwd /opt/my-app --script-file ./deploy.sh
vpsctl exec <alias> --stdin <<'SCRIPT'
set -eu
hostname
uptime
SCRIPT
vpsctl exec <alias> --cwd /opt/my-app 'docker compose pull && docker compose up -d'
vpsctl exec <alias> --cwd /opt/my-app 'docker compose ps'
```

Provide exactly one command string, `--stdin`, or `--script-file`. File/stdin input is limited to 8 MiB. Scripts use POSIX `sh`; explicitly invoke another interpreter when required. `--cwd` must be an absolute remote path. Use `--` before positional arguments beginning with `-`. Global flags still precede `exec`.

Foreground scripts are fully received into a private remote `0600` temporary file before execution; child commands default to `/dev/null` stdin rather than consuming remaining script text. Supply command input with redirection or heredocs inside the script. Temporary scripts are removed on normal exit but may remain after forced termination or host failure.

After writes, perform a focused read-only verification. A nonzero remote command can have partially modified state. Never expose credentials through scripts or output; stdin does not provide automatic secret redaction.

## Durable jobs

```bash
vpsctl exec <alias> --cwd /opt/my-app --detach --script-file ./deploy.sh
vpsctl job status <alias> <job-id>
vpsctl job output <alias> <job-id> [--cursor 0] [--limit 65536]
```

Save `data.job_id` (32 lowercase hexadecimal characters), including on uncertain launches. Reconcile that ID rather than resubmit the script. Jobs use remote `~/.local/state/vpsctl/jobs/<job-id>/`, with private directories and `0600` files; scripts and merged stdout/stderr logs persist. Requires remote Linux procfs, POSIX `sh`, GNU coreutils (`stat`, `dd`, `base64`, `nohup`, etc.) and util-linux `setsid`.

Status uses a completion record, or verifies worker identity against Linux boot ID and process start time. Missing workers without completion records are `unknown`, not success. Jobs survive SSH disconnects, not host reboots. There is no cancel/list/automatic cleanup or log rotation; logs are not capped.

Output defaults to cursor 0 and a 65536-byte limit, with limit range 1..1048576, further bounded by global `--max-output`. `data.cursor`, `data.next_cursor`, and `data.size` count raw log bytes. `data.output_base64` is lossless; `stdout` replaces invalid UTF-8 for display. Advance using `next_cursor`, not character counts. `truncated` indicates more bytes in the observed log; new bytes may arrive later. Reading all available output is not proof of job completion.

## Files

```bash
vpsctl upload <alias> <local-file> <absolute-remote-file> [--overwrite]
vpsctl download <alias> <absolute-remote-file> <local-file> [--overwrite]
vpsctl upload <alias> <local-directory> <absolute-remote-directory> --recursive [--overwrite]
vpsctl download <alias> <absolute-remote-directory> <local-directory> --recursive [--overwrite]
vpsctl upload <alias> <local-file> <absolute-remote-file> --resume [--overwrite]
vpsctl download <alias> <absolute-remote-file> <local-file> --resume [--overwrite]
vpsctl transfer <source-alias> <absolute-source-file> <destination-alias> <absolute-destination-file> [--overwrite]
```

Remote paths must be absolute, non-root, and contain no NUL/CR/LF. Destination parent directories must exist for single-file operations. Confirm all targets and overwrite effects beforehand.

Single-file operations verify SHA-256 and commit via a temporary file in the destination directory. They default to refusing existing destinations and reject unsafe nonregular/symlink destinations. New files use `0600`; set executable or service-readable permissions separately only when authorized. Checksums do not prove application correctness, so verify intended behavior too.

`transfer` streams one regular file through the local machine without a local disk copy or agent forwarding. Both hosts authenticate from the local machine. No direct/hybrid modes, recursive server-to-server copy, or automatic fallback retries.

`--recursive` or `--resume` selects **rsync 3+ on both ends**. Recursive copies transfer directory contents; default behavior skips existing files, while `--overwrite` updates them. Resumption uses partial-file recovery, not an application transaction. Files are private (`0600`, directories `0700`), and the whole tree is not atomic; failure may leave partial updates. Use release directories and a separately authorized switch for atomic deployments. The macOS system rsync may be too old.

## Batch execution

```bash
vpsctl cluster --hosts host-a,host-b --parallel 2 'uptime'
vpsctl cluster --hosts host-a,host-b --parallel 2 --script-file ./check.sh
vpsctl cluster --hosts host-a,host-b --stdin < check.sh
```

Explicit unique hosts are mandatory, at most 256; parallelism defaults to 4 and must be 1..64. Exactly one command/script input is required. There is no implicit all-host selection, environment/tag targeting, or special health-check flag. Results appear in `data` as `{host, result}` entries in requested order. Any unsuccessful member produces `BATCH_FAILED`; inspect every nested result for partial or unknown outcomes before further writes.

## Tunnels

```bash
vpsctl tunnel start <alias> --local-port 15432 --remote-host 127.0.0.1 --remote-port 5432
vpsctl tunnel list
vpsctl tunnel status <alias> --id <tunnel-id>
vpsctl tunnel stop <alias> --id <tunnel-id>
```

All start parameters are required; ports are 1..65535 and remote host is DNS or IPv4. Forwards bind `127.0.0.1`, not public interfaces. Tunnels use an OpenSSH control master and private local state under `~/.ssh/vpsctl-tunnels/`, not a vpsctl daemon. Save `data.id`, including on an uncertain start, then check/stop that ID rather than start another tunnel. `tunnel list` reports recorded configuration only (`live_checked: false`); use `status` to check the master. `status` does not prove the destination service is healthy. No `stop-all` command exists.

## Keys

```bash
vpsctl key add <alias> --public-key ./id_ed25519.pub
vpsctl key verify <alias> --identity ~/.ssh/id_ed25519
vpsctl key rollback <alias> --backup <backup-id>
```

Key writes require explicit target/impact approval and a working pre-existing key-authenticated connection; there is no password bootstrap. Add accepts one OpenSSH public key without `authorized_keys` options and validates it with local `ssh-keygen`. It preserves existing entries and records a backup ID. Backups are remote `~/.ssh/.vpsctl-key-backups/<backup-id>` files; keep returned IDs for recovery.

Verify uses a fresh connection restricted to the named identity rather than a reused multiplexed connection; its encrypted private key must already be unlocked in `ssh-agent`. It refuses `Match` configurations and returns `UNSUPPORTED_CONFIG` for `ProxyJump` rather than flattening the destination identity into jump-host authentication. Normal execution and transfers still support `ProxyJump`. Verify the new key before retiring an old key. Rollback restores the explicitly selected whole `authorized_keys` backup, so later changes could be lost; inspect and approve that impact first. No deploy/migrate or batch-key subcommands exist.

## Troubleshooting

Read `error.code`, `error.message`, and `stderr`. Authentication failures require a provisioned key or unlocked agent, not password prompts. Host-key rejection requires independent fingerprint verification, never bypassing strict checking. Dependency errors require the named system tool. Treat output truncation and unknown outcomes explicitly; do not infer success from empty output, shell process termination, or absent job completion records. Temporary `0600` files protect against other users, not other processes running as the same account; never place secrets in logs or reports.
