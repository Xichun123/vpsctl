"""CLI commands for persistent project profiles."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys
from typing import Any, Sequence

from .store import OPTIONAL_PROJECT_FIELDS, Store, StoreError


_SCALAR_OPTIONS = {
    "description": "项目用途或说明",
    "repo_url": "代码仓库 URL",
    "branch": "预期部署分支",
    "runtime": "运行方式，如 docker-compose/systemd/node",
    "service": "systemd service 名称",
    "compose_file": "相对项目目录的 Compose 文件",
    "deploy_command": "部署命令（只保存，不会在刷新时执行）",
    "restart_command": "重启命令（只保存，不会在刷新时执行）",
    "log_command": "查看日志命令（只保存，不会在刷新时执行）",
    "healthcheck": "健康检查 URL 或说明",
    "notes": "其他注意事项",
}


def _add_profile_options(parser: argparse.ArgumentParser, required: bool) -> None:
    parser.add_argument("--host", dest="host_alias", required=required, help="SSH 主机别名")
    parser.add_argument("--path", dest="remote_path", required=required, help="项目远程绝对路径")
    for field, help_text in _SCALAR_OPTIONS.items():
        parser.add_argument(f"--{field.replace('_', '-')}", dest=field, help=help_text)
    parser.add_argument("--domain", dest="domains", action="append", help="项目域名，可重复")
    parser.add_argument("--tag", dest="tags", action="append", help="项目标签，可重复")
    parser.add_argument(
        "--protect",
        dest="protected_paths",
        action="append",
        help="禁止 Agent 随意覆盖的路径，可重复",
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="vpsctl project", description="管理项目部署档案"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    add_parser = subparsers.add_parser("add", help="新增项目档案")
    add_parser.add_argument("name", help="项目唯一名称")
    _add_profile_options(add_parser, required=True)

    update_parser = subparsers.add_parser("update", help="更新项目档案")
    update_parser.add_argument("name", help="项目名称")
    _add_profile_options(update_parser, required=False)
    update_parser.add_argument(
        "--clear",
        action="append",
        choices=OPTIONAL_PROJECT_FIELDS,
        default=[],
        help="清空可选字段，可重复",
    )

    show_parser = subparsers.add_parser("show", help="查看单个项目")
    show_parser.add_argument("name", help="项目名称")

    list_parser = subparsers.add_parser("list", help="列出项目")
    list_parser.add_argument("--host", dest="host_alias", help="按主机别名过滤")
    list_parser.add_argument("--tag", help="按精确标签过滤")

    delete_parser = subparsers.add_parser("delete", help="删除项目档案")
    delete_parser.add_argument("name", help="项目名称")

    export_parser = subparsers.add_parser("export", help="导出全部项目档案")
    export_parser.add_argument("--output", help="写入 JSON 文件；默认输出到 stdout")

    return parser


def _print_json(data: dict[str, Any], *, error: bool = False) -> None:
    print(
        json.dumps(data, ensure_ascii=False, indent=2),
        file=sys.stderr if error else sys.stdout,
    )


def _provided_profile_values(args: argparse.Namespace) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for field in ("host_alias", "remote_path", *_SCALAR_OPTIONS, "domains", "tags", "protected_paths"):
        value = getattr(args, field, None)
        if value is not None:
            result[field] = value
    return result


def main(argv: Sequence[str] | None = None, store: Store | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(list(argv) if argv is not None else None)
    state = store or Store()

    try:
        if args.command == "add":
            project = state.add_project(args.name, **_provided_profile_values(args))
            _print_json({"success": True, "project": project})
            return 0

        if args.command == "update":
            project = state.update_project(
                args.name,
                _provided_profile_values(args),
                clear_fields=args.clear,
            )
            _print_json({"success": True, "project": project})
            return 0

        if args.command == "show":
            _print_json({"success": True, "project": state.get_project(args.name)})
            return 0

        if args.command == "list":
            projects = state.list_projects(host_alias=args.host_alias, tag=args.tag)
            _print_json({"success": True, "count": len(projects), "projects": projects})
            return 0

        if args.command == "delete":
            state.delete_project(args.name)
            _print_json({"success": True, "message": f"项目 {args.name} 已删除"})
            return 0

        if args.command == "export":
            data = state.export()
            if args.output:
                output_path = Path(args.output).expanduser()
                output_path.write_text(
                    json.dumps(data, ensure_ascii=False, indent=2) + "\n",
                    encoding="utf-8",
                )
                _print_json(
                    {
                        "success": True,
                        "count": len(data["projects"]),
                        "output": str(output_path),
                    }
                )
            else:
                _print_json({"success": True, **data})
            return 0

        parser.error(f"未知命令: {args.command}")
    except (OSError, StoreError) as exc:
        _print_json({"success": False, "error": str(exc)}, error=True)
        return 1

    return 2
