"""
原生 SSH 降级模块

当检测到复杂场景（ProxyCommand、passphrase 等）时，
降级使用原生 ssh 命令而非 Paramiko。
"""

import subprocess
import os
import shlex
import tempfile
from typing import Optional, Dict, Tuple


def _run_ssh_command(
    alias: str,
    remote_command: str,
    timeout: int,
    ssh_config_path: str
) -> subprocess.CompletedProcess:
    """执行一条原生 SSH 远程命令"""
    ssh_cmd = [
        'ssh',
        '-F', ssh_config_path,
        '-o', 'BatchMode=yes',
        '-o', 'StrictHostKeyChecking=accept-new',
        alias,
        remote_command
    ]
    return subprocess.run(
        ssh_cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        encoding='utf-8',
        errors='replace'
    )


def _build_script_exec_command(remote_path: str, runner: Optional[list[str]] = None) -> str:
    """构造执行远端临时脚本的命令"""
    parts = list(runner or ['/bin/sh'])
    parts.append(remote_path)
    return " ".join(shlex.quote(part) for part in parts)


def should_use_native_ssh(ssh_config: dict, metadata: dict = None) -> Tuple[bool, str]:
    """
    检测是否应该使用原生 SSH 而非 Paramiko

    Args:
        ssh_config: SSH 配置字典（从 paramiko.SSHConfig.lookup 获取）
        metadata: 元数据字典（可选）

    Returns:
        (should_fallback, reason) 元组
    """
    reasons = []

    # 检测 ProxyCommand（包括 Cloudflare Tunnel）
    proxy_command = ssh_config.get('proxycommand')
    if proxy_command:
        # Cloudflare Tunnel
        if 'cloudflared' in proxy_command.lower():
            reasons.append("检测到 Cloudflare Tunnel (ProxyCommand)")
        # 其他 ProxyCommand
        else:
            reasons.append(f"检测到 ProxyCommand: {proxy_command}")

    # 检测 ProxyJump（多级跳板机）
    proxy_jump = ssh_config.get('proxyjump')
    if proxy_jump and ',' in proxy_jump:
        # 多级跳板机（单级跳板机 Paramiko 可以处理）
        reasons.append(f"检测到多级跳板机: {proxy_jump}")

    # 检测密钥文件是否需要 passphrase
    identity_file = ssh_config.get('identityfile')
    if identity_file:
        # 如果是列表，取第一个
        if isinstance(identity_file, list):
            identity_file = identity_file[0] if identity_file else None

        if identity_file and _key_has_passphrase(identity_file):
            reasons.append("检测到密钥需要 passphrase（建议使用 ssh-agent）")

    # 检测其他复杂配置
    if ssh_config.get('localforward') or ssh_config.get('remoteforward'):
        reasons.append("检测到端口转发配置")

    if ssh_config.get('dynamicforward'):
        reasons.append("检测到动态端口转发（SOCKS 代理）")

    # 如果有任何复杂场景，建议降级
    if reasons:
        return True, "; ".join(reasons)

    return False, ""


def _key_has_passphrase(key_file: str) -> bool:
    """
    检测密钥文件是否有 passphrase 保护

    注意：这是一个启发式检测，不是 100% 准确
    """
    try:
        key_file = os.path.expanduser(key_file)
        if not os.path.exists(key_file):
            return False

        with open(key_file, 'r') as f:
            content = f.read()

        # 检测加密标记（旧格式）
        if 'ENCRYPTED' in content:
            return True

        # OpenSSH 新格式的加密密钥
        if 'BEGIN OPENSSH PRIVATE KEY' in content:
            # 提取所有 base64 行（排除 BEGIN/END 行）
            lines = content.strip().split('\n')
            base64_lines = [line for line in lines
                           if line and not line.startswith('-----')]

            if base64_lines:
                try:
                    import base64
                    # 合并所有 base64 行后解码
                    base64_content = ''.join(base64_lines)
                    decoded = base64.b64decode(base64_content).decode('latin-1', errors='ignore')

                    # 检查是否包含加密算法标记
                    # 如果包含 'none' 且没有其他加密算法，表示未加密
                    has_encryption = any(marker in decoded for marker in
                                       ['aes128-ctr', 'aes192-ctr', 'aes256-ctr',
                                        'aes128-cbc', 'aes192-cbc', 'aes256-cbc'])

                    if has_encryption:
                        return True

                    # 如果只有 'none'，表示未加密
                    if 'none' in decoded and not has_encryption:
                        return False

                except Exception:
                    pass

        return False
    except Exception:
        return False


def execute_native_ssh(
    alias: str,
    command: str,
    timeout: int = 120,
    ssh_config_path: Optional[str] = None
) -> Dict:
    """
    使用原生 ssh 命令执行远程命令

    Args:
        alias: SSH 别名
        command: 要执行的命令
        timeout: 超时时间（秒）
        ssh_config_path: SSH 配置文件路径（默认 ~/.ssh/config）

    Returns:
        结果字典 {success, exit_code, stdout, stderr}
    """
    if ssh_config_path is None:
        ssh_config_path = os.path.expanduser("~/.ssh/config")

    try:
        result = _run_ssh_command(alias, command, timeout, ssh_config_path)

        return {
            'success': result.returncode == 0,
            'exit_code': result.returncode,
            'stdout': result.stdout,
            'stderr': result.stderr,
            'method': 'native_ssh'
        }

    except subprocess.TimeoutExpired:
        return {
            'success': False,
            'exit_code': -1,
            'stdout': '',
            'stderr': f'命令执行超时（{timeout}秒）',
            'method': 'native_ssh'
        }

    except Exception as e:
        return {
            'success': False,
            'exit_code': -1,
            'stdout': '',
            'stderr': f'执行失败: {str(e)}',
            'method': 'native_ssh'
        }


def execute_native_ssh_script(
    alias: str,
    script_text: str,
    runner: Optional[list[str]] = None,
    timeout: int = 120,
    ssh_config_path: Optional[str] = None
) -> Dict:
    """
    使用原生 ssh/scp 上传脚本到远端临时文件并执行。

    Args:
        alias: SSH 别名
        script_text: 脚本文本
        runner: 解释器命令参数，未指定时默认 /bin/sh
        timeout: 超时时间（秒）
        ssh_config_path: SSH 配置文件路径（默认 ~/.ssh/config）

    Returns:
        结果字典 {success, exit_code, stdout, stderr}
    """
    if ssh_config_path is None:
        ssh_config_path = os.path.expanduser("~/.ssh/config")

    local_path = None
    remote_path = None

    try:
        mktemp_result = _run_ssh_command(
            alias,
            "umask 077 && mktemp /tmp/agent-ssh-script.XXXXXX",
            timeout,
            ssh_config_path,
        )
        if mktemp_result.returncode != 0:
            detail = mktemp_result.stderr.strip() or mktemp_result.stdout.strip() or "unknown error"
            return {
                'success': False,
                'exit_code': mktemp_result.returncode or -1,
                'stdout': '',
                'stderr': f'创建远端临时脚本失败: {detail}',
                'method': 'native_ssh'
            }

        remote_path = mktemp_result.stdout.strip().splitlines()[-1] if mktemp_result.stdout.strip() else ''
        if not remote_path:
            return {
                'success': False,
                'exit_code': -1,
                'stdout': '',
                'stderr': '创建远端临时脚本失败: 未返回路径',
                'method': 'native_ssh'
            }

        fd, local_path = tempfile.mkstemp(prefix='agent-ssh-script-', text=True)
        with os.fdopen(fd, 'w', encoding='utf-8', newline='') as f:
            f.write(script_text)

        scp_cmd = [
            'scp',
            '-F', ssh_config_path,
            '-o', 'BatchMode=yes',
            '-o', 'StrictHostKeyChecking=accept-new',
            local_path,
            f'{alias}:{remote_path}'
        ]
        upload_result = subprocess.run(
            scp_cmd,
            capture_output=True,
            text=True,
            timeout=timeout,
            encoding='utf-8',
            errors='replace'
        )
        if upload_result.returncode != 0:
            detail = upload_result.stderr.strip() or upload_result.stdout.strip() or "unknown error"
            return {
                'success': False,
                'exit_code': upload_result.returncode,
                'stdout': '',
                'stderr': f'上传远端临时脚本失败: {detail}',
                'method': 'native_ssh'
            }

        result = _run_ssh_command(
            alias,
            _build_script_exec_command(remote_path, runner=runner),
            timeout,
            ssh_config_path,
        )
        return {
            'success': result.returncode == 0,
            'exit_code': result.returncode,
            'stdout': result.stdout,
            'stderr': result.stderr,
            'method': 'native_ssh'
        }
    except subprocess.TimeoutExpired:
        return {
            'success': False,
            'exit_code': -1,
            'stdout': '',
            'stderr': f'命令执行超时（{timeout}秒）',
            'method': 'native_ssh'
        }
    except Exception as e:
        return {
            'success': False,
            'exit_code': -1,
            'stdout': '',
            'stderr': f'执行失败: {str(e)}',
            'method': 'native_ssh'
        }
    finally:
        if local_path and os.path.exists(local_path):
            os.remove(local_path)
        if remote_path:
            try:
                _run_ssh_command(
                    alias,
                    f"rm -f -- {shlex.quote(remote_path)}",
                    min(timeout, 10),
                    ssh_config_path,
                )
            except Exception:
                pass


def check_ssh_agent() -> Tuple[bool, str]:
    """
    检查 ssh-agent 是否运行且有密钥

    Returns:
        (is_available, message) 元组
    """
    auth_sock = os.environ.get('SSH_AUTH_SOCK')
    if not auth_sock:
        return False, "ssh-agent 未运行（SSH_AUTH_SOCK 未设置）"

    # 尝试列出密钥
    try:
        result = subprocess.run(
            ['ssh-add', '-l'],
            capture_output=True,
            text=True,
            timeout=5
        )

        if result.returncode == 0:
            # 有密钥
            key_count = len([line for line in result.stdout.strip().split('\n') if line])
            return True, f"ssh-agent 运行中，已加载 {key_count} 个密钥"
        elif result.returncode == 1:
            # agent 运行但没有密钥
            return False, "ssh-agent 运行中，但未加载任何密钥（运行 ssh-add 添加密钥）"
        else:
            return False, f"ssh-agent 状态异常: {result.stderr}"

    except subprocess.TimeoutExpired:
        return False, "ssh-add 命令超时"
    except FileNotFoundError:
        return False, "ssh-add 命令不存在"
    except Exception as e:
        return False, f"检查 ssh-agent 失败: {str(e)}"
