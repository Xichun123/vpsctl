"""Read-only remote discovery for hosts and registered projects."""

from __future__ import annotations

import json
from pathlib import Path
import shlex
import subprocess
import sys
from typing import Any

from .store import Store


_RUNTIME_DIR = Path(__file__).resolve().parent / "_runtime"
_MARKER = "__VPSCTL_SECTION__:"


HOST_SCRIPT = f"""#!/bin/sh
section() {{ printf '\\n{_MARKER}%s\\n' "$1"; }}
section hostname; hostname 2>/dev/null || true
section remote_time; date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || true
section kernel; uname -a 2>/dev/null || true
section os_release; cat /etc/os-release 2>/dev/null || true
section uptime; uptime 2>/dev/null || true
section cpu_count; (getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null) | head -1
section memory; cat /proc/meminfo 2>/dev/null | head -30 || true
section disk_root; df -Pk / 2>/dev/null | tail -1 || true
section addresses; hostname -I 2>/dev/null || true
section listening; (ss -lntup 2>/dev/null || netstat -lntup 2>/dev/null || true) | head -200
section docker_version; docker version --format '{{{{.Server.Version}}}}' 2>/dev/null || true
section docker_containers; docker ps -a --format '{{{{json .}}}}' 2>/dev/null | head -200 || true
section systemd_failed; systemctl --failed --no-legend --no-pager 2>/dev/null | head -100 || true
"""


class DiscoveryError(RuntimeError):
    """Raised when the remote discovery script cannot produce a result."""


class RuntimeExecutor:
    """Execute a script through the existing ssh_execute runtime."""

    def __init__(self, runtime_dir: Path | None = None):
        self.runtime_dir = runtime_dir or _RUNTIME_DIR

    def execute_script(self, alias: str, script: str, timeout: int = 60) -> dict[str, Any]:
        script_path = self.runtime_dir / "ssh_execute.py"
        try:
            completed = subprocess.run(
                [
                    sys.executable,
                    str(script_path),
                    alias,
                    "--stdin",
                    "--timeout",
                    str(timeout),
                ],
                input=script,
                text=True,
                capture_output=True,
                timeout=timeout + 15,
                check=False,
            )
        except subprocess.TimeoutExpired as exc:
            raise DiscoveryError(f"SSH 探测超时（{timeout} 秒）") from exc
        except OSError as exc:
            raise DiscoveryError(f"无法启动 SSH 探测: {exc}") from exc

        try:
            result = json.loads(completed.stdout)
        except json.JSONDecodeError as exc:
            details = completed.stderr.strip() or completed.stdout.strip() or "无输出"
            raise DiscoveryError(f"SSH 运行时返回了无效 JSON: {details[:500]}") from exc

        if completed.returncode != 0 or not result.get("success"):
            error = result.get("stderr") or completed.stderr.strip() or "远程探测失败"
            raise DiscoveryError(str(error))
        return result


def parse_sections(output: str) -> dict[str, str]:
    sections: dict[str, list[str]] = {}
    current: str | None = None
    for line in output.splitlines():
        if line.startswith(_MARKER):
            current = line[len(_MARKER):].strip()
            if current:
                sections.setdefault(current, [])
            continue
        if current:
            sections[current].append(line)
    return {name: "\n".join(lines).strip() for name, lines in sections.items()}


def _parse_os_release(raw: str) -> dict[str, str]:
    result: dict[str, str] = {}
    for line in raw.splitlines():
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
            value = value[1:-1]
        result[key.lower()] = value
    return result


def _parse_memory(raw: str) -> dict[str, int]:
    result: dict[str, int] = {}
    for line in raw.splitlines():
        if ":" not in line:
            continue
        key, value = line.split(":", 1)
        token = value.strip().split()[0] if value.strip() else ""
        if token.isdigit():
            result[f"{key.lower()}_kib"] = int(token)
    return result


def _parse_disk(raw: str) -> dict[str, Any]:
    parts = raw.split()
    if len(parts) < 6:
        return {"raw": raw} if raw else {}
    try:
        return {
            "filesystem": parts[0],
            "total_kib": int(parts[1]),
            "used_kib": int(parts[2]),
            "available_kib": int(parts[3]),
            "used_percent": parts[4],
            "mountpoint": " ".join(parts[5:]),
        }
    except ValueError:
        return {"raw": raw}


def _parse_json_lines(raw: str) -> list[Any]:
    result: list[Any] = []
    for line in raw.splitlines():
        try:
            result.append(json.loads(line))
        except json.JSONDecodeError:
            if line.strip():
                result.append({"raw": line.strip()})
    return result


def discover_host(alias: str, executor: RuntimeExecutor, timeout: int = 60) -> dict[str, Any]:
    result = executor.execute_script(alias, HOST_SCRIPT, timeout=timeout)
    sections = parse_sections(result.get("stdout", ""))
    cpu_count = sections.get("cpu_count", "")
    return {
        "hostname": sections.get("hostname", ""),
        "remote_time": sections.get("remote_time", ""),
        "kernel": sections.get("kernel", ""),
        "os": _parse_os_release(sections.get("os_release", "")),
        "uptime": sections.get("uptime", ""),
        "cpu_count": int(cpu_count) if cpu_count.isdigit() else None,
        "memory": _parse_memory(sections.get("memory", "")),
        "disk_root": _parse_disk(sections.get("disk_root", "")),
        "addresses": sections.get("addresses", "").split(),
        "listening_sockets": sections.get("listening", "").splitlines(),
        "docker": {
            "available": bool(sections.get("docker_version")),
            "version": sections.get("docker_version", ""),
            "containers": _parse_json_lines(sections.get("docker_containers", "")),
        },
        "systemd_failed_units": sections.get("systemd_failed", "").splitlines(),
    }


def build_project_script(project: dict[str, Any]) -> str:
    remote_path = shlex.quote(project["remote_path"])
    service = shlex.quote(project.get("service", ""))
    configured_compose = shlex.quote(project.get("compose_file", ""))
    return f"""#!/bin/sh
section() {{ printf '\\n{_MARKER}%s\\n' "$1"; }}
project_path={remote_path}
section path
printf '%s\\n' "$project_path"
section path_exists
if [ -d "$project_path" ]; then printf 'true\\n'; else printf 'false\\n'; exit 0; fi
cd -- "$project_path" || exit 0
section directory_entries
(find . -mindepth 1 -maxdepth 1 -printf '%f\\n' 2>/dev/null || ls -1A 2>/dev/null || true) | head -200
section git_root
git rev-parse --show-toplevel 2>/dev/null || true
section git_remote
git remote get-url origin 2>/dev/null || true
section git_branch
git branch --show-current 2>/dev/null || true
section git_commit
git rev-parse HEAD 2>/dev/null || true
section git_status
git status --short --branch 2>/dev/null | head -200 || true
configured_compose={configured_compose}
if [ -n "$configured_compose" ]; then compose_file="$configured_compose"; else
  compose_file=''
  for candidate in compose.yaml compose.yml docker-compose.yaml docker-compose.yml; do
    if [ -f "$candidate" ]; then compose_file="$candidate"; break; fi
  done
fi
section compose_file
printf '%s\\n' "$compose_file"
section compose_ps
if [ -n "$compose_file" ] && command -v docker >/dev/null 2>&1; then
  docker compose -f "$compose_file" ps --format json 2>/dev/null | head -200 || true
fi
configured_service={service}
section service
printf '%s\\n' "$configured_service"
section service_state
if [ -n "$configured_service" ] && command -v systemctl >/dev/null 2>&1; then
  systemctl show "$configured_service" --no-pager \\
    --property=LoadState,ActiveState,SubState,MainPID,ExecMainStartTimestamp 2>/dev/null || true
fi
"""


def _parse_key_values(raw: str) -> dict[str, str]:
    result: dict[str, str] = {}
    for line in raw.splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            result[key.strip()] = value.strip()
    return result


def discover_project(
    project: dict[str, Any], executor: RuntimeExecutor, timeout: int = 60
) -> dict[str, Any]:
    result = executor.execute_script(
        project["host_alias"], build_project_script(project), timeout=timeout
    )
    sections = parse_sections(result.get("stdout", ""))
    return {
        "path": sections.get("path", project["remote_path"]),
        "path_exists": sections.get("path_exists") == "true",
        "directory_entries": sections.get("directory_entries", "").splitlines(),
        "git": {
            "root": sections.get("git_root", ""),
            "remote": sections.get("git_remote", ""),
            "branch": sections.get("git_branch", ""),
            "commit": sections.get("git_commit", ""),
            "status": sections.get("git_status", "").splitlines(),
        },
        "compose": {
            "file": sections.get("compose_file", ""),
            "services": _parse_json_lines(sections.get("compose_ps", "")),
        },
        "service": {
            "name": sections.get("service", ""),
            "state": _parse_key_values(sections.get("service_state", "")),
        },
    }


def refresh_host(
    store: Store, alias: str, executor: RuntimeExecutor, timeout: int = 60
) -> dict[str, Any]:
    try:
        data = discover_host(alias, executor, timeout=timeout)
        return store.save_snapshot("host", alias, alias, True, data=data)
    except DiscoveryError as exc:
        return store.save_snapshot("host", alias, alias, False, error=str(exc))


def refresh_project(
    store: Store, name: str, executor: RuntimeExecutor, timeout: int = 60
) -> dict[str, Any]:
    project = store.get_project(name)
    try:
        data = discover_project(project, executor, timeout=timeout)
        return store.save_snapshot(
            "project", name, project["host_alias"], True, data=data
        )
    except DiscoveryError as exc:
        return store.save_snapshot(
            "project", name, project["host_alias"], False, error=str(exc)
        )
