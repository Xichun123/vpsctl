"""CLI entry points for remote refresh and Agent context bundles."""

from __future__ import annotations

import argparse
import json
import sys
from typing import Any, Sequence

from .context import build_context
from .discovery import RuntimeExecutor, refresh_host, refresh_project
from .store import Store, StoreError


def _print_json(data: dict[str, Any], *, error: bool = False) -> None:
    print(
        json.dumps(data, ensure_ascii=False, indent=2),
        file=sys.stderr if error else sys.stdout,
    )


def build_refresh_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="vpsctl refresh", description="通过只读 SSH 探测刷新状态快照"
    )
    subparsers = parser.add_subparsers(dest="scope", required=True)
    host_parser = subparsers.add_parser("host", help="刷新一台主机")
    host_parser.add_argument("alias", help="SSH 主机别名")
    host_parser.add_argument("--timeout", type=int, default=60, help="SSH 超时秒数")

    project_parser = subparsers.add_parser("project", help="刷新项目及其主机")
    project_parser.add_argument("name", help="项目名称")
    project_parser.add_argument("--timeout", type=int, default=60, help="每次 SSH 探测超时秒数")
    return parser


def refresh_main(
    argv: Sequence[str] | None = None,
    *,
    store: Store | None = None,
    executor: RuntimeExecutor | None = None,
) -> int:
    args = build_refresh_parser().parse_args(list(argv) if argv is not None else None)
    state = store or Store()
    remote = executor or RuntimeExecutor()

    try:
        if args.scope == "host":
            results = [refresh_host(state, args.alias, remote, timeout=args.timeout)]
            selector = {"type": "host", "value": args.alias}
        else:
            project = state.get_project(args.name)
            results = [
                refresh_host(state, project["host_alias"], remote, timeout=args.timeout),
                refresh_project(state, args.name, remote, timeout=args.timeout),
            ]
            selector = {"type": "project", "value": args.name}
    except (OSError, StoreError, ValueError) as exc:
        _print_json({"success": False, "error": str(exc)}, error=True)
        return 1

    success = all(result["success"] for result in results)
    _print_json({"success": success, "selector": selector, "results": results})
    return 0 if success else 1


def build_context_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="vpsctl context",
        description="输出供 AI Agent 使用的项目与服务器上下文",
    )
    selector = parser.add_mutually_exclusive_group(required=True)
    selector.add_argument("--project", dest="project_name", help="按项目名称获取上下文")
    selector.add_argument("--host", dest="host_alias", help="按 SSH 主机别名获取上下文")
    parser.add_argument(
        "--refresh", action="store_true", help="输出前执行只读 SSH 探测"
    )
    parser.add_argument(
        "--max-age",
        type=int,
        default=None,
        help="可选：快照超过该秒数时标记为 stale；默认不按时间过期",
    )
    parser.add_argument(
        "--full",
        dest="compact",
        action="store_false",
        help="输出完整探测快照；默认输出项目紧凑上下文",
    )
    parser.set_defaults(compact=True)
    parser.add_argument("--timeout", type=int, default=60, help="每次 SSH 探测超时秒数")
    return parser


def context_main(
    argv: Sequence[str] | None = None,
    *,
    store: Store | None = None,
    executor: RuntimeExecutor | None = None,
) -> int:
    args = build_context_parser().parse_args(list(argv) if argv is not None else None)
    state = store or Store()
    refresh_results: list[dict[str, Any]] = []

    try:
        if args.refresh:
            remote = executor or RuntimeExecutor()
            if args.project_name:
                project = state.get_project(args.project_name)
                refresh_results.extend(
                    [
                        refresh_host(
                            state, project["host_alias"], remote, timeout=args.timeout
                        ),
                        refresh_project(
                            state, args.project_name, remote, timeout=args.timeout
                        ),
                    ]
                )
            else:
                refresh_results.append(
                    refresh_host(state, args.host_alias, remote, timeout=args.timeout)
                )
                for project in state.list_projects(host_alias=args.host_alias):
                    refresh_results.append(
                        refresh_project(
                            state, project["name"], remote, timeout=args.timeout
                        )
                    )

        context = build_context(
            state,
            project_name=args.project_name,
            host_alias=args.host_alias,
            max_age_seconds=args.max_age,
            compact=args.compact,
        )
    except (OSError, StoreError, ValueError) as exc:
        _print_json({"success": False, "error": str(exc)}, error=True)
        return 1

    if args.refresh:
        context["refresh"] = {
            "success": all(result["success"] for result in refresh_results),
            "results": refresh_results,
        }
        if not context["refresh"]["success"]:
            context["success"] = False

    _print_json(context)
    return 0 if context["success"] else 1
