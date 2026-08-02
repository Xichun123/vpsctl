# vpsctl command reference

Use `vpsctl <command> --help` when this reference and the installed CLI differ. The installed CLI is authoritative.

## Local context and profiles

```bash
vpsctl project list [--host <alias>] [--tag <tag>]
vpsctl project show <name>
vpsctl context --project <name>
vpsctl context --host <alias>
```

Create and update project profiles:

```bash
vpsctl project add <name> --host <alias> --path <absolute-path> \
  [--runtime <runtime>] [--service <unit>] [--compose-file <file>] \
  [--domain <domain>] [--tag <tag>] [--protect <path>] \
  [--deploy-command <command>] [--restart-command <command>] \
  [--log-command <command>] [--healthcheck <url-or-note>] [--notes <notes>]

vpsctl project update <name> [fields...]
vpsctl project export --output <file>
```

Explicitly refresh a baseline only when needed:

```bash
vpsctl refresh project <name>
vpsctl refresh host <alias>
vpsctl context --project <name> --refresh
```

Refresh uses built-in read-only probes. It does not run custom deploy, restart, or log commands stored in a profile.

## Host inventory

```bash
vpsctl list
vpsctl host list --environment production
vpsctl find <query>
vpsctl host create --alias <alias> --host <address> --user <user> --key <private-key>
vpsctl host update <alias> --description <description> --tags <tag...>
vpsctl host delete <alias>
vpsctl host export --output <file>
```

Host entries are stored in `~/.ssh/config`. Host create, update, and delete operations modify that file.

## Read-only execution

```bash
vpsctl exec <alias> "<simple-read-only-command>"
vpsctl exec <alias> "<command>" --timeout <seconds>
vpsctl exec <alias> --script-file ./readonly-check.sh
```

For complex stdin scripts:

```bash
vpsctl exec <alias> --stdin <<'SCRIPT'
set -eu
hostname
uptime
SCRIPT
```

## Recorded modifications

```bash
vpsctl apply <project> --kind deploy --summary "Deploy release" \
  "docker compose pull && docker compose up -d"

vpsctl apply <project> --kind restart --summary "Restart service" \
  "systemctl restart app.service"

vpsctl apply <project> --stdin --kind config --summary "Update configuration" <<'SCRIPT'
set -eu
# modification commands
SCRIPT
```

Supplemental records:

```bash
vpsctl change add <project> --kind upload --summary "Upload frontend build"
vpsctl change list --project <project> --limit 20
```

## File transfer

```bash
vpsctl upload <alias> <local-path> <remote-path> [--resume] [--recursive]
vpsctl download <alias> <remote-path> <local-path> [--resume] [--recursive]
vpsctl transfer <source-alias> <source-path> <destination-alias> <destination-path> \
  [--mode auto|direct|stream|hybrid] [--use-rsync]
```

Transfer modes:

- `direct`: source server sends directly to destination; useful for large data when hosts can reach each other.
- `stream`: data passes through the local machine; useful when servers cannot connect directly.
- `hybrid`: tries direct and falls back to streaming.
- `auto`: lets vpsctl select a mode.

## Batch operations

```bash
vpsctl cluster "<command>" --parallel
vpsctl cluster "<command>" --hosts "host-a,host-b" --parallel
vpsctl cluster "<command>" --environment production --parallel
vpsctl cluster "<command>" --tags "web,nginx" --parallel --health-check
```

## Tunnels

```bash
vpsctl tunnel start <alias> --remote-port <port>
vpsctl tunnel start <alias> --local-port <port> --remote-host <host> --remote-port <port>
vpsctl tunnel list
vpsctl tunnel status <tunnel-id>
vpsctl tunnel stop <tunnel-id>
vpsctl tunnel stop-all <alias>
```

Tunnels listen on localhost and support `ProxyJump`.

## Connection daemon

```bash
vpsctl daemon start <alias> [--idle-timeout <seconds>]
vpsctl daemon status <alias>
vpsctl daemon stop <alias>
```

The `exec` command chooses a connection mode based on authentication. If a daemon connection is unhealthy, stop it and retry; use `--no-daemon` for a one-off direct connection.

## SSH key management

```bash
vpsctl key add --host <alias> --key <public-key-file>
vpsctl key add --hosts "host-a,host-b" --key <public-key-file>
vpsctl key verify --host <alias> --key <public-key-file>
vpsctl key rollback --host <alias>
vpsctl key deploy <alias> --pubkey-file <file> --key-name <name>
vpsctl key migrate <alias> --key-file <private-key-file-name>
```

Key writes and authentication migration require explicit approval and targeted verification.

## SSH configuration maintenance

```bash
vpsctl config migrate
vpsctl config annotate
vpsctl config fix
```

These commands may rewrite `~/.ssh/config`; back it up and confirm scope before running them.

## Troubleshooting

```bash
vpsctl --help
vpsctl <command> --help
vpsctl <group> <subcommand> --help
```

Always inspect JSON `success`, `exit_code`, `stdout`, and `stderr`. If host-key verification reports a changed key, stop and verify the server fingerprint rather than removing the warning blindly.
