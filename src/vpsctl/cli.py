"""Unified command-line interface for the ssh-skill runtime."""

from __future__ import annotations

import os
from pathlib import Path
import subprocess
import sys
from typing import Sequence

from . import __version__


_RUNTIME_DIR = Path(__file__).resolve().parent / "_runtime"

_DIRECT_COMMANDS = {
    "exec": "ssh_execute.py",
    "upload": "ssh_upload.py",
    "download": "ssh_download.py",
    "transfer": "ssh_server_transfer.py",
    "cluster": "ssh_cluster.py",
    "tunnel": "ssh_tunnel.py",
    "daemon": "ssh_daemon.py",
}

_HOST_COMMANDS = {
    "list": "list-servers",
    "find": "find",
    "create": "create",
    "add": "create",
    "update": "update",
    "delete": "delete",
    "export": "export",
}

_KEY_COMMANDS = {
    "add": ("ssh_key_manager.py", "add"),
    "verify": ("ssh_key_manager.py", "verify"),
    "rollback": ("ssh_key_manager.py", "rollback"),
    "deploy": ("deploy_pubkey.py", None),
    "migrate": ("migrate_to_key_auth.py", None),
}

_CONFIG_COMMANDS = {
    "migrate": "migrate_to_ssh_config.py",
    "annotate": "add_comments_to_config.py",
    "fix": "fix_ssh_config.py",
}

_TOP_LEVEL_HELP = f"""vpsctl {__version__} - 面向 AI Agent 的统一 SSH/VPS 命令行工具

用法:
  vpsctl <命令> [参数...]

核心命令:
  host       管理 ~/.ssh/config 中的服务器
  exec       在单台服务器上执行命令或脚本
  upload     上传文件或目录
  download   下载文件或目录
  transfer   在两台服务器之间传输文件
  cluster    对多台服务器批量执行命令
  tunnel     管理本地 SSH 端口转发
  daemon     管理 SSH 长连接守护进程
  key        添加、验证、回滚、部署或迁移 SSH 密钥
  config     迁移、注释或修复 SSH 配置
  inventory  采集服务器系统信息
  project    管理项目部署档案
  apply      执行项目修改并自动记录变更
  change     补记或查询项目变更日志
  refresh    显式校准远端状态快照
  context    快速读取本地项目上下文

快捷命令:
  list       等同于 `vpsctl host list`
  find       等同于 `vpsctl host find`

示例:
  vpsctl list
  vpsctl find production
  vpsctl project list
  vpsctl context --project my-app
  vpsctl apply my-app --kind deploy --summary "部署新版本" "docker compose up -d"
  vpsctl exec prod-web-01 "hostname && uptime"
  cat deploy.sh | vpsctl exec prod-web-01 --stdin
  vpsctl upload prod-web-01 ./dist /var/www/app --recursive
  vpsctl transfer old /data new /data --mode hybrid
  vpsctl tunnel start prod-db --remote-port 5432

运行 `vpsctl <命令> --help` 查看具体参数。
默认 SSH 配置: ~/.ssh/config
默认状态数据库: ~/.vpsctl/state.db
"""

_GROUP_HELP = {
    "host": """用法: vpsctl host <list|find|create|update|delete|export> [参数...]

示例:
  vpsctl host list --environment production
  vpsctl host find web
  vpsctl host create --alias web-01 --host 192.0.2.10 --user root --key ~/.ssh/id_ed25519
  vpsctl host update web-01 --tags web nginx
  vpsctl host delete web-01
""",
    "key": """用法: vpsctl key <add|verify|rollback|deploy|migrate> [参数...]

说明:
  add/verify/rollback  安全管理 authorized_keys
  deploy               部署指定公钥
  migrate              将主机从密码认证迁移到密钥认证
""",
    "config": """用法: vpsctl config <migrate|annotate|fix> [参数...]

说明:
  migrate   从旧 JSON 配置迁移到 ~/.ssh/config
  annotate  为现有 ~/.ssh/config 添加标准元数据注释
  fix       使用旧 JSON 数据修复 ~/.ssh/config 元数据

警告: annotate 和 fix 会写入 SSH 配置，请先做好备份。
""",
    "inventory": """用法: vpsctl inventory refresh

连接全部已配置主机，采集系统信息并更新 SSH 配置元数据。
""",
    "project": """用法: vpsctl project <add|update|show|list|delete|export> [参数...]

示例:
  vpsctl project add my-app --host prod-web --path /opt/my-app --runtime docker-compose
  vpsctl project list --host prod-web
  vpsctl project show my-app
  vpsctl project update my-app --service my-app.service
""",
    "apply": """用法: vpsctl apply <项目> --summary <摘要> [--kind 类型] <命令>

执行修改命令并自动写入变更日志；只保存摘要和命令 SHA-256，不保存命令原文。
""",
    "change": """用法: vpsctl change <add|list> [参数...]

补记上传等非 apply 操作，或查询项目/主机的变更历史。
""",
    "refresh": """用法: vpsctl refresh <host|project> <名称> [--timeout 秒]

显式通过只读 SSH 重新校准基线；日常任务不需要调用。
""",
    "context": """用法: vpsctl context (--project <名称> | --host <别名>) [--full] [--refresh]

默认只读取本地紧凑上下文和变更日志，不联网、不按时间自动过期。
""",
    "tunnel": "用法: vpsctl tunnel <start|list|status|stop|stop-all> [参数...]\n",
    "daemon": "用法: vpsctl daemon <start|status|stop> [参数...]\n",
}

_LEAF_HELP = {
    ("config", "annotate"): """用法: vpsctl config annotate

为 ~/.ssh/config 中尚无标准元数据的 Host 添加注释。该命令会写入配置文件。
""",
    ("config", "fix"): """用法: vpsctl config fix

使用 ~/.ssh/server_config 中的旧 JSON 数据修复 ~/.ssh/config。该命令会写入配置文件。
""",
    ("inventory", "refresh"): """用法: vpsctl inventory refresh

采集全部主机的操作系统、CPU、内存和磁盘信息，并更新 ~/.ssh/config。
""",
}


class CLIError(ValueError):
    """Raised when the vpsctl command tree cannot be resolved."""


def _is_help(args: Sequence[str]) -> bool:
    return any(arg in {"-h", "--help"} for arg in args)


def resolve_command(argv: Sequence[str]) -> tuple[str, list[str]]:
    """Translate a vpsctl command into a vendored runtime script invocation."""
    if not argv:
        raise CLIError("缺少命令")

    command, *rest = argv

    if command in {"list", "find"}:
        return "ssh_config_manager_v3.py", [
            _HOST_COMMANDS[command],
            *rest,
        ]

    if command in _DIRECT_COMMANDS:
        return _DIRECT_COMMANDS[command], list(rest)

    if command == "host":
        if not rest:
            raise CLIError("host 命令需要一个子命令")
        subcommand, *subargs = rest
        if subcommand not in _HOST_COMMANDS:
            raise CLIError(f"未知的 host 子命令: {subcommand}")
        return "ssh_config_manager_v3.py", [
            _HOST_COMMANDS[subcommand],
            *subargs,
        ]

    if command == "key":
        if not rest:
            raise CLIError("key 命令需要一个子命令")
        subcommand, *subargs = rest
        if subcommand not in _KEY_COMMANDS:
            raise CLIError(f"未知的 key 子命令: {subcommand}")
        script, runtime_subcommand = _KEY_COMMANDS[subcommand]
        prefix = [runtime_subcommand] if runtime_subcommand else []
        return script, [*prefix, *subargs]

    if command == "config":
        if not rest:
            raise CLIError("config 命令需要一个子命令")
        subcommand, *subargs = rest
        if subcommand not in _CONFIG_COMMANDS:
            raise CLIError(f"未知的 config 子命令: {subcommand}")
        return _CONFIG_COMMANDS[subcommand], list(subargs)

    if command == "inventory":
        if not rest:
            raise CLIError("inventory 命令需要一个子命令")
        subcommand, *subargs = rest
        if subcommand != "refresh":
            raise CLIError(f"未知的 inventory 子命令: {subcommand}")
        return "update_server_info.py", list(subargs)

    raise CLIError(f"未知命令: {command}")


def _print_context_help(argv: Sequence[str]) -> bool:
    """Print vpsctl-owned help and return whether execution should stop."""
    if not argv or argv[0] in {"help", "-h", "--help"}:
        print(_TOP_LEVEL_HELP)
        return True

    if argv[0] in _GROUP_HELP and len(argv) == 1:
        print(_GROUP_HELP[argv[0]])
        return True

    if argv[0] in _GROUP_HELP and len(argv) == 2 and argv[1] in {"-h", "--help"}:
        print(_GROUP_HELP[argv[0]])
        return True

    if len(argv) >= 2 and (argv[0], argv[1]) in _LEAF_HELP and _is_help(argv[2:]):
        print(_LEAF_HELP[(argv[0], argv[1])])
        return True

    return False


def run_runtime(script_name: str, args: Sequence[str]) -> int:
    """Run one runtime script with inherited stdio and return its exit code."""
    script_path = _RUNTIME_DIR / script_name
    if not script_path.is_file():
        print(f"vpsctl: 内部运行时缺失: {script_path}", file=sys.stderr)
        return 70

    env = os.environ.copy()
    env.setdefault("PYTHONUNBUFFERED", "1")

    try:
        completed = subprocess.run(
            [sys.executable, str(script_path), *args],
            env=env,
            check=False,
        )
        return completed.returncode
    except KeyboardInterrupt:
        return 130
    except OSError as exc:
        print(f"vpsctl: 无法启动 SSH 运行时: {exc}", file=sys.stderr)
        return 70


def main(argv: Sequence[str] | None = None) -> int:
    args = list(sys.argv[1:] if argv is None else argv)

    if args == ["--version"] or args == ["version"]:
        print(f"vpsctl {__version__}")
        return 0

    if _print_context_help(args):
        return 0

    if args[0] == "project":
        from .project_cli import main as project_main

        try:
            return project_main(args[1:])
        except SystemExit as exc:
            return int(exc.code or 0)

    if args[0] in {"refresh", "context"}:
        from .context_cli import context_main, refresh_main

        handler = refresh_main if args[0] == "refresh" else context_main
        try:
            return handler(args[1:])
        except SystemExit as exc:
            return int(exc.code or 0)

    if args[0] in {"apply", "change"}:
        from .apply_cli import apply_main, change_main

        handler = apply_main if args[0] == "apply" else change_main
        try:
            return handler(args[1:])
        except SystemExit as exc:
            return int(exc.code or 0)

    try:
        script_name, runtime_args = resolve_command(args)
    except CLIError as exc:
        print(f"vpsctl: {exc}", file=sys.stderr)
        print("运行 `vpsctl --help` 查看可用命令。", file=sys.stderr)
        return 2

    return run_runtime(script_name, runtime_args)
