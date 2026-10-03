# Core
- Single Go package main. main.go: JSON CLI entry and App.Run dispatcher; runner.go: SSH execution; hosts.go: inventory; files.go: transfer; jobs.go: durable jobs; management.go: tunnels/keys; result.go: shared Result.
- completed is not success; require zero exit_code and no error. running only acknowledges launch. unknown must never trigger blind mutation retries; local cancellation does not prove remote termination.
- SSH config is trusted executable input; verified known_hosts and noninteractive key/agent auth are required. Never weaken host verification.
- CLI and skills/vpsctl are separate installations/releases.
- Toolchain constraints: `mem:tech_stack`; change conventions: `mem:conventions`; runnable checks: `mem:suggested_commands`; delivery checks: `mem:task_completion`.
