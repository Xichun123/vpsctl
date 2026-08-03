"""Tracked mutating operations and project change journal commands."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys
from typing import Any, Sequence

from ._runtime.lib.utils import omit_empty_stderr
from .store import CHANGE_KINDS, Store, StoreError


_RUNTIME_DIR = Path(__file__).resolve().parent / "_runtime"


def _print_json(data: dict[str, Any], *, error: bool = False) -> None:
    print(
        json.dumps(omit_empty_stderr(data), ensure_ascii=False, indent=2),
        file=sys.stderr if error else sys.stdout,
    )


def build_apply_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="vpsctl apply",
        description="执行项目修改命令，并自动写入不含命令原文的变更日志",
    )
    parser.add_argument("project", help="项目名称；主机别名从档案读取")
    parser.add_argument("command", nargs="?", help="要执行的远程命令")
    parser.add_argument("--summary", required=True, help="写入档案的变更摘要")
    parser.add_argument(
        "--kind", choices=sorted(CHANGE_KINDS), default="other", help="变更类型"
    )
    parser.add_argument("--details", default="", help="可选的补充说明，不要包含密钥")
    parser.add_argument("--timeout", type=int, default=60, help="远程命令超时秒数")
    parser.add_argument("--no-daemon", action="store_true", help="禁用 SSH daemon")
    parser.add_argument("--stdin", action="store_true", help="从 stdin 读取远程脚本")
    parser.add_argument("--script-file", help="读取本地脚本文件并在远端执行")
    return parser


def _resolve_payload(
    args: argparse.Namespace, stdin_text: str | None
) -> tuple[str, bytes, str | None, list[str]]:
    sources = [args.command is not None, args.stdin, args.script_file is not None]
    if sum(bool(source) for source in sources) != 1:
        raise StoreError("command、--stdin 和 --script-file 必须且只能选择一种")

    runtime_args: list[str] = []
    runtime_input: str | None = None
    if args.command is not None:
        operation = "command"
        payload = args.command.encode("utf-8")
        runtime_args.append(args.command)
    elif args.stdin:
        operation = "stdin-script"
        runtime_input = sys.stdin.read() if stdin_text is None else stdin_text
        if not runtime_input.strip():
            raise StoreError("stdin 脚本内容为空")
        payload = runtime_input.encode("utf-8")
        runtime_args.append("--stdin")
    else:
        script_path = Path(args.script_file).expanduser()
        payload = script_path.read_bytes()
        operation = f"script-file:{script_path.name}"
        runtime_args.extend(["--script-file", str(script_path)])

    return operation, payload, runtime_input, runtime_args


def apply_main(
    argv: Sequence[str] | None = None,
    *,
    store: Store | None = None,
    runtime_dir: Path | None = None,
    stdin_text: str | None = None,
) -> int:
    parser = build_apply_parser()
    argument_list = list(argv) if argv is not None else None
    args = parser.parse_intermixed_args(argument_list)
    state = store or Store()

    try:
        project = state.get_project(args.project)
        operation, payload, runtime_input, runtime_args = _resolve_payload(args, stdin_text)
    except (OSError, StoreError) as exc:
        _print_json({"success": False, "error": str(exc)}, error=True)
        return 1

    digest = hashlib.sha256(payload).hexdigest()
    command = [
        sys.executable,
        str((runtime_dir or _RUNTIME_DIR) / "ssh_execute.py"),
        project["host_alias"],
        *runtime_args,
        "--timeout",
        str(args.timeout),
    ]
    if args.no_daemon:
        command.append("--no-daemon")

    try:
        completed = subprocess.run(
            command,
            input=runtime_input,
            text=True,
            capture_output=True,
            check=False,
        )
        try:
            result = json.loads(completed.stdout)
        except json.JSONDecodeError:
            result = {
                "success": False,
                "exit_code": completed.returncode,
                "stdout": completed.stdout,
                "stderr": completed.stderr or "SSH 运行时返回了无效 JSON",
            }
        success = completed.returncode == 0 and bool(result.get("success"))
        exit_code = result.get("exit_code", completed.returncode)
    except OSError as exc:
        result = {
            "success": False,
            "exit_code": -1,
            "stdout": "",
            "stderr": f"无法启动 SSH 运行时: {exc}",
        }
        success = False
        exit_code = -1

    change = state.add_change(
        args.project,
        kind=args.kind,
        summary=args.summary,
        details=args.details,
        operation=operation,
        payload_sha256=digest,
        success=success,
        exit_code=exit_code if isinstance(exit_code, int) else None,
    )
    output = {**result, "success": success, "change": change}
    _print_json(output, error=False)
    return 0 if success else 1


def build_change_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="vpsctl change", description="补记或查询项目变更日志"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    add_parser = subparsers.add_parser("add", help="补记上传、迁移等变更")
    add_parser.add_argument("project", help="项目名称")
    add_parser.add_argument("--summary", required=True, help="变更摘要")
    add_parser.add_argument("--kind", choices=sorted(CHANGE_KINDS), default="other")
    add_parser.add_argument("--details", default="", help="补充说明，不要包含密钥")
    add_parser.add_argument("--operation", default="manual", help="操作类型")
    add_parser.add_argument("--failed", action="store_true", help="记录为失败或可能部分完成")
    add_parser.add_argument("--exit-code", type=int)

    list_parser = subparsers.add_parser("list", help="查询变更日志")
    selector = list_parser.add_mutually_exclusive_group(required=True)
    selector.add_argument("--project", dest="project_name", help="项目名称")
    selector.add_argument("--host", dest="host_alias", help="主机别名")
    list_parser.add_argument("--limit", type=int, default=20)
    return parser


def change_main(
    argv: Sequence[str] | None = None, *, store: Store | None = None
) -> int:
    args = build_change_parser().parse_args(list(argv) if argv is not None else None)
    state = store or Store()
    try:
        if args.command == "add":
            change = state.add_change(
                args.project,
                kind=args.kind,
                summary=args.summary,
                details=args.details,
                operation=args.operation,
                success=not args.failed,
                exit_code=args.exit_code,
            )
            _print_json({"success": True, "change": change})
        else:
            changes = state.list_changes(
                project_name=args.project_name,
                host_alias=args.host_alias,
                limit=args.limit,
            )
            _print_json({"success": True, "count": len(changes), "changes": changes})
        return 0
    except (OSError, StoreError) as exc:
        _print_json({"success": False, "error": str(exc)}, error=True)
        return 1
