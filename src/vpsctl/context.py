"""Build redacted, freshness-aware context bundles for AI agents."""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

from .store import Store


def _parse_time(value: str) -> datetime:
    normalized = value.replace("Z", "+00:00")
    parsed = datetime.fromisoformat(normalized)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)


def snapshot_context(
    store: Store,
    scope_type: str,
    scope_key: str,
    max_age_seconds: int | None = None,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = now or datetime.now(timezone.utc)
    latest_attempt = store.latest_snapshot(scope_type, scope_key)
    latest_success = store.latest_snapshot(scope_type, scope_key, successful_only=True)

    if latest_success:
        age_seconds = max(
            0, int((current_time - _parse_time(latest_success["observed_at"])).total_seconds())
        )
        if max_age_seconds is None:
            status = "tracked"
        else:
            status = "fresh" if age_seconds <= max_age_seconds else "stale"
        observation = latest_success["data"]
    else:
        age_seconds = None
        status = "missing"
        observation = None

    if latest_attempt and not latest_attempt["success"]:
        status = "refresh_failed"

    freshness = {
        "status": status,
        "max_age_seconds": max_age_seconds,
        "age_seconds": age_seconds,
        "observed_at": latest_success["observed_at"] if latest_success else None,
        "last_attempt_at": latest_attempt["observed_at"] if latest_attempt else None,
        "last_attempt_success": latest_attempt["success"] if latest_attempt else None,
        "last_error": latest_attempt["error"] if latest_attempt and not latest_attempt["success"] else "",
    }
    return {"freshness": freshness, "observation": observation}


def safe_host_profile(alias: str) -> dict[str, Any]:
    """Load an SSH host without exposing password metadata."""
    from ._runtime.lib.config_v3 import SSHConfigLoaderV3

    loader = SSHConfigLoaderV3()
    config = loader.load_ssh_config(alias)
    metadata = loader.load_metadata(alias)
    identity_files = config.get("identityfile") or []
    if not isinstance(identity_files, list):
        identity_files = [identity_files]
    has_password = bool(metadata.get("password"))
    has_key = bool(identity_files)
    if has_password and has_key:
        auth = "password+key"
    elif has_password:
        auth = "password"
    elif has_key:
        auth = "key"
    else:
        auth = "default"

    try:
        port = int(config.get("port", 22))
    except (TypeError, ValueError):
        port = 22

    return {
        "alias": alias,
        "hostname": config.get("hostname", ""),
        "user": config.get("user", ""),
        "port": port,
        "identity_files": identity_files,
        "proxy_jump": config.get("proxyjump", ""),
        "forward_agent": str(config.get("forwardagent", "no")).lower()
        in {"yes", "true", "1"},
        "auth": auth,
        "description": metadata.get("description", ""),
        "environment": metadata.get("environment", "unknown"),
        "tags": metadata.get("tags", []),
        "location": metadata.get("location", ""),
    }


def _compact_host_observation(observation: dict[str, Any] | None) -> dict[str, Any] | None:
    if observation is None:
        return None
    memory = observation.get("memory") or {}
    docker = observation.get("docker") or {}
    os_info = observation.get("os") or {}
    return {
        "hostname": observation.get("hostname", ""),
        "os": {
            "pretty_name": os_info.get("pretty_name", ""),
            "id": os_info.get("id", ""),
            "version_id": os_info.get("version_id", ""),
        },
        "kernel": observation.get("kernel", ""),
        "cpu_count": observation.get("cpu_count"),
        "memory": {
            key: memory.get(key)
            for key in ("memtotal_kib", "memavailable_kib", "swaptotal_kib", "swapfree_kib")
            if key in memory
        },
        "disk_root": observation.get("disk_root") or {},
        "docker": {
            "available": docker.get("available", False),
            "version": docker.get("version", ""),
            "container_count": len(docker.get("containers") or []),
        },
        "systemd_failed_units": observation.get("systemd_failed_units") or [],
    }


def _compact_project_observation(observation: dict[str, Any] | None) -> dict[str, Any] | None:
    if observation is None:
        return None
    git = observation.get("git") or {}
    compose = observation.get("compose") or {}
    status = git.get("status") or []
    return {
        "path": observation.get("path", ""),
        "path_exists": observation.get("path_exists"),
        "git": {
            "remote": git.get("remote", ""),
            "branch": git.get("branch", ""),
            "commit": git.get("commit", ""),
            "dirty": len(status) > 1 or (status and not status[0].startswith("##")),
            "status_count": len(status),
        },
        "compose": {
            "file": compose.get("file", ""),
            "services": compose.get("services") or [],
        },
        "service": observation.get("service") or {},
    }


def build_context(
    store: Store,
    *,
    project_name: str | None = None,
    host_alias: str | None = None,
    max_age_seconds: int | None = None,
    compact: bool = True,
    now: datetime | None = None,
) -> dict[str, Any]:
    if bool(project_name) == bool(host_alias):
        raise ValueError("必须且只能指定 project_name 或 host_alias")
    if max_age_seconds is not None and max_age_seconds < 0:
        raise ValueError("max_age_seconds 不能为负数")

    if project_name:
        projects = [store.get_project(project_name)]
        alias = projects[0]["host_alias"]
        selector = {"type": "project", "value": project_name}
    else:
        alias = str(host_alias)
        projects = store.list_projects(host_alias=alias)
        selector = {"type": "host", "value": alias}

    warnings: list[str] = []
    try:
        profile: dict[str, Any] = safe_host_profile(alias)
    except (FileNotFoundError, ImportError, ValueError) as exc:
        profile = {"alias": alias, "error": str(exc)}
        warnings.append(f"无法读取 SSH 主机配置: {exc}")

    host_state = snapshot_context(store, "host", alias, max_age_seconds, now=now)
    if compact:
        host_state["observation"] = _compact_host_observation(host_state["observation"])
    host_entry = {
        "profile": profile,
        **host_state,
        "recent_changes": store.list_changes(host_alias=alias, limit=20),
    }
    healthy_statuses = {"tracked", "fresh"}
    if host_state["freshness"]["status"] not in healthy_statuses:
        warnings.append(
            f"主机 {alias} 状态为 {host_state['freshness']['status']}"
        )

    project_entries = []
    for project in projects:
        state = snapshot_context(
            store, "project", project["name"], max_age_seconds, now=now
        )
        if compact:
            state["observation"] = _compact_project_observation(state["observation"])
        baseline_time = state["freshness"]["observed_at"]
        changes = store.list_changes(project_name=project["name"], limit=20)
        changes_since_baseline = (
            store.list_changes(project_name=project["name"], since=baseline_time, limit=1000)
            if baseline_time
            else changes
        )
        project_entries.append(
            {
                "profile": project,
                **state,
                "recent_changes": changes,
                "changes_since_baseline": len(changes_since_baseline),
            }
        )
        if state["freshness"]["status"] not in healthy_statuses:
            warnings.append(
                f"项目 {project['name']} 状态为 {state['freshness']['status']}"
            )

    statuses = [host_state["freshness"]["status"]] + [
        item["freshness"]["status"] for item in project_entries
    ]
    return {
        "success": "refresh_failed" not in statuses and "error" not in profile,
        "complete": bool(statuses) and all(status in healthy_statuses for status in statuses),
        "mode": "compact" if compact else "full",
        "generated_at": (now or datetime.now(timezone.utc)).isoformat(timespec="seconds"),
        "selector": selector,
        "host": host_entry,
        "projects": project_entries,
        "warnings": warnings,
    }
