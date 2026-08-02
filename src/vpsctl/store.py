"""Persistent project profiles and observation snapshots."""

from __future__ import annotations

from contextlib import contextmanager
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import sqlite3
from typing import Any, Iterable, Iterator


PROJECT_SCALAR_FIELDS = (
    "host_alias",
    "remote_path",
    "description",
    "repo_url",
    "branch",
    "runtime",
    "service",
    "compose_file",
    "deploy_command",
    "restart_command",
    "log_command",
    "healthcheck",
    "notes",
)
PROJECT_LIST_FIELDS = ("domains", "tags", "protected_paths")
PROJECT_FIELDS = PROJECT_SCALAR_FIELDS + PROJECT_LIST_FIELDS
OPTIONAL_PROJECT_FIELDS = tuple(
    field for field in PROJECT_FIELDS if field not in {"host_alias", "remote_path"}
)
CHANGE_KINDS = {
    "deploy",
    "config",
    "restart",
    "data",
    "upload",
    "maintenance",
    "other",
}


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def default_db_path() -> Path:
    override = os.environ.get("VPSCTL_DB")
    if override:
        return Path(override).expanduser()
    return Path("~/.vpsctl/state.db").expanduser()


class StoreError(ValueError):
    """Raised for invalid or conflicting state-store operations."""


class Store:
    """Small SQLite store safe for concurrent CLI processes."""

    def __init__(self, path: str | os.PathLike[str] | None = None):
        self.path = Path(path).expanduser() if path is not None else default_db_path()
        parent_existed = self.path.parent.exists()
        self.path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        if not parent_existed or self.path.parent.name == ".vpsctl":
            try:
                self.path.parent.chmod(0o700)
            except OSError:
                pass
        self._initialize()

    def _connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(self.path, timeout=10)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA foreign_keys = ON")
        connection.execute("PRAGMA busy_timeout = 10000")
        return connection

    @contextmanager
    def _connection(self) -> Iterator[sqlite3.Connection]:
        connection = self._connect()
        try:
            with connection:
                yield connection
        finally:
            connection.close()

    def _initialize(self) -> None:
        with self._connection() as connection:
            connection.execute("PRAGMA journal_mode = WAL")
            connection.executescript(
                """
                CREATE TABLE IF NOT EXISTS projects (
                    name TEXT PRIMARY KEY,
                    host_alias TEXT NOT NULL,
                    remote_path TEXT NOT NULL,
                    description TEXT NOT NULL DEFAULT '',
                    repo_url TEXT NOT NULL DEFAULT '',
                    branch TEXT NOT NULL DEFAULT '',
                    runtime TEXT NOT NULL DEFAULT '',
                    service TEXT NOT NULL DEFAULT '',
                    compose_file TEXT NOT NULL DEFAULT '',
                    deploy_command TEXT NOT NULL DEFAULT '',
                    restart_command TEXT NOT NULL DEFAULT '',
                    log_command TEXT NOT NULL DEFAULT '',
                    healthcheck TEXT NOT NULL DEFAULT '',
                    notes TEXT NOT NULL DEFAULT '',
                    domains_json TEXT NOT NULL DEFAULT '[]',
                    tags_json TEXT NOT NULL DEFAULT '[]',
                    protected_paths_json TEXT NOT NULL DEFAULT '[]',
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL
                );

                CREATE TABLE IF NOT EXISTS snapshots (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    scope_type TEXT NOT NULL CHECK(scope_type IN ('host', 'project')),
                    scope_key TEXT NOT NULL,
                    host_alias TEXT NOT NULL,
                    observed_at TEXT NOT NULL,
                    success INTEGER NOT NULL,
                    data_json TEXT NOT NULL DEFAULT '{}',
                    error TEXT NOT NULL DEFAULT ''
                );

                CREATE INDEX IF NOT EXISTS snapshots_scope_time
                    ON snapshots(scope_type, scope_key, observed_at DESC, id DESC);

                CREATE TABLE IF NOT EXISTS changes (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    project_name TEXT NOT NULL,
                    host_alias TEXT NOT NULL,
                    kind TEXT NOT NULL,
                    summary TEXT NOT NULL,
                    details TEXT NOT NULL DEFAULT '',
                    operation TEXT NOT NULL DEFAULT '',
                    payload_sha256 TEXT NOT NULL DEFAULT '',
                    success INTEGER NOT NULL,
                    exit_code INTEGER,
                    created_at TEXT NOT NULL,
                    FOREIGN KEY(project_name) REFERENCES projects(name) ON DELETE CASCADE
                );

                CREATE INDEX IF NOT EXISTS changes_project_time
                    ON changes(project_name, created_at DESC, id DESC);
                CREATE INDEX IF NOT EXISTS changes_host_time
                    ON changes(host_alias, created_at DESC, id DESC);
                """
            )
            connection.execute("PRAGMA user_version = 2")
        try:
            self.path.chmod(0o600)
        except OSError:
            pass

    @staticmethod
    def _normalize_list(values: Iterable[str] | None) -> list[str]:
        if values is None:
            return []
        result: list[str] = []
        seen: set[str] = set()
        for value in values:
            item = value.strip()
            if item and item not in seen:
                result.append(item)
                seen.add(item)
        return result

    @staticmethod
    def _project_from_row(row: sqlite3.Row) -> dict[str, Any]:
        project = {
            key: row[key]
            for key in (
                "name",
                *PROJECT_SCALAR_FIELDS,
                "created_at",
                "updated_at",
            )
        }
        for field in PROJECT_LIST_FIELDS:
            project[field] = json.loads(row[f"{field}_json"])
        return project

    def add_project(self, name: str, **values: Any) -> dict[str, Any]:
        name = name.strip()
        if not name:
            raise StoreError("项目名称不能为空")
        host_alias = str(values.get("host_alias", "")).strip()
        remote_path = str(values.get("remote_path", "")).strip()
        if not host_alias:
            raise StoreError("host_alias 不能为空")
        if not remote_path.startswith("/"):
            raise StoreError("remote_path 必须是绝对路径")

        now = utc_now()
        row: dict[str, Any] = {"name": name, "created_at": now, "updated_at": now}
        for field in PROJECT_SCALAR_FIELDS:
            row[field] = str(values.get(field, "")).strip()
        for field in PROJECT_LIST_FIELDS:
            row[f"{field}_json"] = json.dumps(
                self._normalize_list(values.get(field)), ensure_ascii=False
            )

        columns = list(row)
        placeholders = ", ".join("?" for _ in columns)
        try:
            with self._connection() as connection:
                connection.execute(
                    f"INSERT INTO projects ({', '.join(columns)}) VALUES ({placeholders})",
                    [row[column] for column in columns],
                )
        except sqlite3.IntegrityError as exc:
            raise StoreError(f"项目已存在: {name}") from exc
        return self.get_project(name)

    def get_project(self, name: str) -> dict[str, Any]:
        with self._connection() as connection:
            row = connection.execute(
                "SELECT * FROM projects WHERE name = ?", (name,)
            ).fetchone()
        if row is None:
            raise StoreError(f"项目不存在: {name}")
        return self._project_from_row(row)

    def list_projects(
        self, host_alias: str | None = None, tag: str | None = None
    ) -> list[dict[str, Any]]:
        query = "SELECT * FROM projects"
        params: list[Any] = []
        if host_alias:
            query += " WHERE host_alias = ?"
            params.append(host_alias)
        query += " ORDER BY name COLLATE NOCASE"
        with self._connection() as connection:
            projects = [
                self._project_from_row(row)
                for row in connection.execute(query, params).fetchall()
            ]
        if tag:
            projects = [project for project in projects if tag in project["tags"]]
        return projects

    def update_project(
        self, name: str, changes: dict[str, Any], clear_fields: Iterable[str] = ()
    ) -> dict[str, Any]:
        current = self.get_project(name)
        update_values: dict[str, Any] = {}

        for field, value in changes.items():
            if field not in PROJECT_FIELDS:
                raise StoreError(f"不可更新的字段: {field}")
            if field in PROJECT_LIST_FIELDS:
                update_values[f"{field}_json"] = json.dumps(
                    self._normalize_list(value), ensure_ascii=False
                )
            else:
                normalized = str(value).strip()
                if field == "remote_path" and not normalized.startswith("/"):
                    raise StoreError("remote_path 必须是绝对路径")
                if field == "host_alias" and not normalized:
                    raise StoreError("host_alias 不能为空")
                update_values[field] = normalized

        for field in clear_fields:
            if field not in OPTIONAL_PROJECT_FIELDS:
                raise StoreError(f"字段不能清空: {field}")
            update_values[f"{field}_json" if field in PROJECT_LIST_FIELDS else field] = (
                "[]" if field in PROJECT_LIST_FIELDS else ""
            )

        if not update_values:
            raise StoreError("没有提供需要更新的字段")

        update_values["updated_at"] = utc_now()
        assignments = ", ".join(f"{column} = ?" for column in update_values)
        with self._connection() as connection:
            connection.execute(
                f"UPDATE projects SET {assignments} WHERE name = ?",
                [*update_values.values(), name],
            )
        return self.get_project(name)

    def delete_project(self, name: str) -> None:
        self.get_project(name)
        with self._connection() as connection:
            connection.execute("DELETE FROM projects WHERE name = ?", (name,))
            connection.execute(
                "DELETE FROM snapshots WHERE scope_type = 'project' AND scope_key = ?",
                (name,),
            )

    def save_snapshot(
        self,
        scope_type: str,
        scope_key: str,
        host_alias: str,
        success: bool,
        data: dict[str, Any] | None = None,
        error: str = "",
        observed_at: str | None = None,
        keep: int = 50,
    ) -> dict[str, Any]:
        if scope_type not in {"host", "project"}:
            raise StoreError(f"不支持的快照类型: {scope_type}")
        timestamp = observed_at or utc_now()
        with self._connection() as connection:
            cursor = connection.execute(
                """
                INSERT INTO snapshots
                    (scope_type, scope_key, host_alias, observed_at, success, data_json, error)
                VALUES (?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    scope_type,
                    scope_key,
                    host_alias,
                    timestamp,
                    int(success),
                    json.dumps(data or {}, ensure_ascii=False),
                    error,
                ),
            )
            if keep > 0:
                connection.execute(
                    """
                    DELETE FROM snapshots
                    WHERE scope_type = ? AND scope_key = ? AND id NOT IN (
                        SELECT id FROM snapshots
                        WHERE scope_type = ? AND scope_key = ?
                        ORDER BY observed_at DESC, id DESC LIMIT ?
                    )
                    """,
                    (scope_type, scope_key, scope_type, scope_key, keep),
                )
            snapshot_id = cursor.lastrowid
        return self.get_snapshot(snapshot_id)

    def get_snapshot(self, snapshot_id: int) -> dict[str, Any]:
        with self._connection() as connection:
            row = connection.execute(
                "SELECT * FROM snapshots WHERE id = ?", (snapshot_id,)
            ).fetchone()
        if row is None:
            raise StoreError(f"快照不存在: {snapshot_id}")
        return self._snapshot_from_row(row)

    @staticmethod
    def _snapshot_from_row(row: sqlite3.Row) -> dict[str, Any]:
        return {
            "id": row["id"],
            "scope_type": row["scope_type"],
            "scope_key": row["scope_key"],
            "host_alias": row["host_alias"],
            "observed_at": row["observed_at"],
            "success": bool(row["success"]),
            "data": json.loads(row["data_json"]),
            "error": row["error"],
        }

    def latest_snapshot(
        self, scope_type: str, scope_key: str, successful_only: bool = False
    ) -> dict[str, Any] | None:
        query = "SELECT * FROM snapshots WHERE scope_type = ? AND scope_key = ?"
        if successful_only:
            query += " AND success = 1"
        query += " ORDER BY observed_at DESC, id DESC LIMIT 1"
        with self._connection() as connection:
            row = connection.execute(query, (scope_type, scope_key)).fetchone()
        return self._snapshot_from_row(row) if row else None

    @staticmethod
    def _change_from_row(row: sqlite3.Row) -> dict[str, Any]:
        return {
            "id": row["id"],
            "project_name": row["project_name"],
            "host_alias": row["host_alias"],
            "kind": row["kind"],
            "summary": row["summary"],
            "details": row["details"],
            "operation": row["operation"],
            "payload_sha256": row["payload_sha256"],
            "success": bool(row["success"]),
            "exit_code": row["exit_code"],
            "created_at": row["created_at"],
        }

    def add_change(
        self,
        project_name: str,
        *,
        kind: str,
        summary: str,
        details: str = "",
        operation: str = "",
        payload_sha256: str = "",
        success: bool = True,
        exit_code: int | None = None,
        created_at: str | None = None,
    ) -> dict[str, Any]:
        project = self.get_project(project_name)
        if kind not in CHANGE_KINDS:
            raise StoreError(f"不支持的变更类型: {kind}")
        summary = summary.strip()
        if not summary:
            raise StoreError("变更摘要不能为空")
        digest = payload_sha256.strip().lower()
        if digest and (len(digest) != 64 or any(char not in "0123456789abcdef" for char in digest)):
            raise StoreError("payload_sha256 必须是 64 位十六进制摘要")

        with self._connection() as connection:
            cursor = connection.execute(
                """
                INSERT INTO changes
                    (project_name, host_alias, kind, summary, details, operation,
                     payload_sha256, success, exit_code, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    project_name,
                    project["host_alias"],
                    kind,
                    summary,
                    details.strip(),
                    operation.strip(),
                    digest,
                    int(success),
                    exit_code,
                    created_at or utc_now(),
                ),
            )
            change_id = cursor.lastrowid
        with self._connection() as connection:
            row = connection.execute(
                "SELECT * FROM changes WHERE id = ?", (change_id,)
            ).fetchone()
        if row is None:
            raise StoreError("写入变更日志失败")
        return self._change_from_row(row)

    def list_changes(
        self,
        *,
        project_name: str | None = None,
        host_alias: str | None = None,
        limit: int = 20,
        since: str | None = None,
    ) -> list[dict[str, Any]]:
        if limit < 1 or limit > 1000:
            raise StoreError("limit 必须在 1 到 1000 之间")
        conditions: list[str] = []
        params: list[Any] = []
        if project_name:
            conditions.append("project_name = ?")
            params.append(project_name)
        if host_alias:
            conditions.append("host_alias = ?")
            params.append(host_alias)
        if since:
            conditions.append("created_at > ?")
            params.append(since)
        query = "SELECT * FROM changes"
        if conditions:
            query += " WHERE " + " AND ".join(conditions)
        query += " ORDER BY created_at DESC, id DESC LIMIT ?"
        params.append(limit)
        with self._connection() as connection:
            rows = connection.execute(query, params).fetchall()
        return [self._change_from_row(row) for row in rows]

    def export(self) -> dict[str, Any]:
        return {
            "version": 2,
            "exported_at": utc_now(),
            "projects": self.list_projects(),
            "changes": self.list_changes(limit=1000),
        }
