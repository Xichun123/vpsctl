# Security Policy

## Supported versions

Security fixes are applied to the latest released minor version.

| Version | Supported |
| --- | --- |
| 0.3.x | Yes |
| Earlier versions | No |

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use the repository's GitHub Security Advisory form:

https://github.com/Xichun123/vpsctl/security/advisories/new

Include the affected command or module, reproduction steps, impact, and any proposed mitigation. Remove private keys, passwords, tokens, IP inventories, and other sensitive infrastructure data from the report.

You should receive an acknowledgement within seven days. Maintainers will coordinate validation, remediation, and disclosure through the advisory.

## Operational security

`vpsctl` executes commands on remote systems and can modify SSH configuration, keys, files, services, and deployment state. Review commands before execution, use least-privilege accounts, protect `~/.ssh` and `~/.vpsctl`, and verify host-key change warnings rather than bypassing them.
