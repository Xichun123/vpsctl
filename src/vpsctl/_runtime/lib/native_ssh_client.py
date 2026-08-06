"""
原生 SSH 客户端模块

使用原生 ssh/scp 命令，充分利用系统级 SSH 特性：
- ControlMaster 自动连接复用
- ProxyJump 原生跳板机支持
- ForwardAgent 完美支持
- 性能更好，功能更完整

适用场景：密钥认证（无密码）
"""

import subprocess
import os
import tempfile
import shlex
from typing import Optional, Iterator
from dataclasses import dataclass

try:
    from .host_key_policy import openssh_accept_new_args
except ImportError:
    from host_key_policy import openssh_accept_new_args


@dataclass
class SSHResult:
    """SSH命令执行结果"""
    success: bool
    stdout: str
    stderr: str
    exit_code: int


class NativeSSHClient:
    """基于原生 SSH 命令的客户端

    使用系统原生 ssh/scp 命令，支持：
    - 密钥认证
    - ProxyJump 跳板机
    - ForwardAgent 代理转发
    - ControlMaster 连接复用
    """

    def __init__(
        self,
        host: str,
        user: str,
        port: int = 22,
        key_file: Optional[str] = None,
        timeout: int = 30,
        proxy_jump: Optional[str] = None,
        forward_agent: bool = False,
        alias: Optional[str] = None
    ):
        """
        初始化原生 SSH 客户端

        Args:
            host: SSH服务器地址
            user: SSH用户名
            port: SSH端口，默认22
            key_file: SSH私钥文件路径
            timeout: 连接超时时间（秒），默认30
            proxy_jump: ProxyJump 配置（如 "user@jumphost:port"）
            forward_agent: 是否启用 SSH agent forwarding
            alias: 服务器别名（用于 ControlMaster）
        """
        self.host = host
        self.user = user
        self.port = port
        self.key_file = key_file
        self.timeout = timeout
        self.proxy_jump = proxy_jump
        self.forward_agent = forward_agent
        self.alias = alias or f"{user}@{host}:{port}"

    def _build_ssh_base_args(self) -> list:
        """构建 SSH 基础参数"""
        args = [
            "ssh",
            "-p", str(self.port),
            *openssh_accept_new_args(),
            "-o", f"ConnectTimeout={self.timeout}",
        ]

        # 密钥文件
        if self.key_file:
            args.extend(["-i", os.path.expanduser(self.key_file)])

        # ProxyJump
        if self.proxy_jump:
            args.extend(["-o", f"ProxyJump={self.proxy_jump}"])

        # ForwardAgent
        if self.forward_agent:
            args.extend(["-o", "ForwardAgent=yes"])

        # ControlMaster（连接复用）
        # 当前默认禁用，避免与跳板机/多进程场景相互干扰。
        # control_path = self._get_control_path()
        # args.extend([
        #     "-o", "ControlMaster=auto",
        #     "-o", f"ControlPath={control_path}",
        #     "-o", "ControlPersist=600",  # 保持10分钟
        # ])

        return args

    def _get_control_path(self) -> str:
        """获取 ControlMaster socket 路径"""
        # 使用临时目录
        temp_dir = tempfile.gettempdir()
        # 使用别名作为标识，避免路径过长
        safe_alias = self.alias.replace('/', '_').replace('@', '_').replace(':', '_')
        return os.path.join(temp_dir, f"ssh-control-{safe_alias}")

    def _run_ssh_command(self, remote_command: str, timeout: Optional[int] = None) -> subprocess.CompletedProcess:
        """执行单条远程命令"""
        args = self._build_ssh_base_args()
        args.append(f"{self.user}@{self.host}")
        args.append(remote_command)
        return subprocess.run(
            args,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding='utf-8',
            errors='replace',
            timeout=timeout or self.timeout
        )

    @staticmethod
    def _build_script_exec_command(remote_path: str, runner: Optional[list[str]] = None) -> str:
        """构造执行远端临时脚本的命令"""
        parts = list(runner or ['/bin/sh'])
        parts.append(remote_path)
        return " ".join(shlex.quote(part) for part in parts)

    def execute(self, command: str) -> SSHResult:
        """
        执行SSH命令

        Args:
            command: 要执行的命令

        Returns:
            SSHResult对象，包含执行结果
        """
        try:
            result = self._run_ssh_command(command)

            return SSHResult(
                success=(result.returncode == 0),
                stdout=result.stdout,
                stderr=result.stderr,
                exit_code=result.returncode
            )
        except subprocess.TimeoutExpired:
            return SSHResult(
                success=False,
                stdout="",
                stderr=f"Command timeout after {self.timeout} seconds",
                exit_code=-1
            )
        except Exception as e:
            return SSHResult(
                success=False,
                stdout="",
                stderr=f"Execution error: {str(e)}",
                exit_code=-1
            )

    def execute_script(self, script_text: str, runner: Optional[list[str]] = None,
                       timeout: Optional[int] = None) -> SSHResult:
        """
        上传脚本到远端临时文件并执行，避免脚本源码占用 stdin。

        Args:
            script_text: 脚本文本
            runner: 解释器命令参数，未指定时默认 /bin/sh
            timeout: 超时时间（秒）

        Returns:
            SSHResult对象
        """
        actual_timeout = timeout if timeout is not None else self.timeout
        local_path = None
        remote_path = None

        try:
            mktemp_result = self._run_ssh_command(
                "umask 077 && mktemp /tmp/agent-ssh-script.XXXXXX",
                timeout=actual_timeout,
            )
            if mktemp_result.returncode != 0:
                detail = mktemp_result.stderr.strip() or mktemp_result.stdout.strip() or "unknown error"
                return SSHResult(
                    success=False,
                    stdout="",
                    stderr=f"Create remote temp file failed: {detail}",
                    exit_code=mktemp_result.returncode or -1
                )

            remote_path = mktemp_result.stdout.strip().splitlines()[-1] if mktemp_result.stdout.strip() else ""
            if not remote_path:
                return SSHResult(
                    success=False,
                    stdout="",
                    stderr="Create remote temp file failed: empty path returned",
                    exit_code=-1
                )

            fd, local_path = tempfile.mkstemp(prefix='agent-ssh-script-', text=True)
            with os.fdopen(fd, 'w', encoding='utf-8', newline='') as f:
                f.write(script_text)

            upload_result = self.upload(local_path, remote_path, timeout=actual_timeout, show_progress=False)
            if not upload_result.success:
                detail = upload_result.stderr or upload_result.stdout or "unknown error"
                return SSHResult(
                    success=False,
                    stdout="",
                    stderr=f"Upload script failed: {detail}",
                    exit_code=upload_result.exit_code
                )

            result = self._run_ssh_command(
                self._build_script_exec_command(remote_path, runner=runner),
                timeout=actual_timeout,
            )
            return SSHResult(
                success=(result.returncode == 0),
                stdout=result.stdout,
                stderr=result.stderr,
                exit_code=result.returncode
            )
        except subprocess.TimeoutExpired:
            return SSHResult(
                success=False,
                stdout="",
                stderr=f"Command timeout after {actual_timeout} seconds",
                exit_code=-1
            )
        except Exception as e:
            return SSHResult(
                success=False,
                stdout="",
                stderr=f"Execution error: {str(e)}",
                exit_code=-1
            )
        finally:
            if local_path and os.path.exists(local_path):
                os.remove(local_path)
            if remote_path:
                try:
                    self._run_ssh_command(
                        f"rm -f -- {shlex.quote(remote_path)}",
                        timeout=min(actual_timeout, 10),
                    )
                except Exception:
                    pass

    def upload(self, local_path: str, remote_path: str, timeout: Optional[int] = None, show_progress: bool = True) -> SSHResult:
        """
        上传文件到远程服务器

        Args:
            local_path: 本地文件路径
            remote_path: 远程文件路径
            timeout: 文件传输总超时时间（秒），None 表示不限时
            show_progress: 是否显示传输进度

        Returns:
            SSHResult对象，包含操作结果
        """
        # 检查本地文件是否存在
        if not os.path.exists(local_path):
            return SSHResult(
                success=False,
                stdout="",
                stderr=f"Local file not found: {local_path}",
                exit_code=-1
            )

        try:
            args = ["scp"]

            # 基本参数
            args.extend(["-P", str(self.port)])
            args.extend(openssh_accept_new_args())
            args.extend(["-o", f"ConnectTimeout={self.timeout}"])

            # 密钥文件
            if self.key_file:
                args.extend(["-i", os.path.expanduser(self.key_file)])

            # ProxyJump
            if self.proxy_jump:
                args.extend(["-o", f"ProxyJump={self.proxy_jump}"])

            # ControlMaster（复用 SSH 连接）
            # control_path = self._get_control_path()
            # args.extend([
            #     "-o", "ControlMaster=auto",
            #     "-o", f"ControlPath={control_path}",
            #     "-o", "ControlPersist=600",
            # ])

            # 源和目标
            args.append(local_path)
            args.append(f"{self.user}@{self.host}:{remote_path}")

            result = subprocess.run(
                args,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                encoding='utf-8',
                errors='replace',
                timeout=timeout
            )

            return SSHResult(
                success=(result.returncode == 0),
                stdout=f"File uploaded: {local_path} -> {remote_path}" if result.returncode == 0 else result.stdout,
                stderr=result.stderr if result.returncode != 0 else "",
                exit_code=result.returncode
            )
        except subprocess.TimeoutExpired:
            return SSHResult(
                success=False,
                stdout="",
                stderr=(
                    f"Upload timeout after {timeout} seconds"
                    if timeout is not None else
                    "Upload timed out"
                ),
                exit_code=-1
            )
        except Exception as e:
            return SSHResult(
                success=False,
                stdout="",
                stderr=f"Upload error: {str(e)}",
                exit_code=-1
            )

    def download(self, remote_path: str, local_path: str, timeout: Optional[int] = None, show_progress: bool = True) -> SSHResult:
        """
        从远程服务器下载文件

        Args:
            remote_path: 远程文件路径
            local_path: 本地文件路径
            timeout: 超时时间（秒），None 表示根据文件大小自动计算
            show_progress: 是否显示传输进度

        Returns:
            SSHResult对象，包含操作结果
        """
        try:
            # 确保本地目录存在
            local_dir = os.path.dirname(local_path)
            if local_dir and not os.path.exists(local_dir):
                os.makedirs(local_dir, exist_ok=True)

            # 如果没有指定超时，使用默认值（下载时无法提前知道文件大小）
            actual_timeout = timeout if timeout is not None else 600  # 默认 10 分钟

            args = ["scp"]

            # 基本参数
            args.extend(["-P", str(self.port)])
            args.extend(openssh_accept_new_args())

            # 密钥文件
            if self.key_file:
                args.extend(["-i", os.path.expanduser(self.key_file)])

            # ProxyJump
            if self.proxy_jump:
                args.extend(["-o", f"ProxyJump={self.proxy_jump}"])

            # ControlMaster（复用 SSH 连接）
            # control_path = self._get_control_path()
            # args.extend([
            #     "-o", "ControlMaster=auto",
            #     "-o", f"ControlPath={control_path}",
            #     "-o", "ControlPersist=600",
            # ])

            # 源和目标
            args.append(f"{self.user}@{self.host}:{remote_path}")
            args.append(local_path)

            result = subprocess.run(
                args,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                encoding='utf-8',
                errors='replace',
                timeout=actual_timeout
            )

            return SSHResult(
                success=(result.returncode == 0),
                stdout=f"File downloaded: {remote_path} -> {local_path}" if result.returncode == 0 else result.stdout,
                stderr=result.stderr if result.returncode != 0 else "",
                exit_code=result.returncode
            )
        except subprocess.TimeoutExpired:
            return SSHResult(
                success=False,
                stdout="",
                stderr=f"Download timeout after {timeout or self.timeout} seconds",
                exit_code=-1
            )
        except Exception as e:
            return SSHResult(
                success=False,
                stdout="",
                stderr=f"Download error: {str(e)}",
                exit_code=-1
            )

    def test_connection(self) -> SSHResult:
        """
        测试SSH连接

        Returns:
            SSHResult对象，包含测试结果
        """
        return self.execute("echo 'Connection OK'")

    def execute_stream(self, command: str, timeout: Optional[int] = None) -> Iterator[str]:
        """
        实时流式执行命令，逐行返回输出

        Args:
            command: 要执行的命令
            timeout: 总超时时间（秒），默认使用实例的timeout

        Yields:
            命令输出的每一行
        """
        try:
            args = self._build_ssh_base_args()
            args.append(f"{self.user}@{self.host}")
            args.append(command)

            process = subprocess.Popen(
                args,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                encoding='utf-8',
                errors='replace'
            )

            # 逐行读取输出
            if process.stdout:
                for line in iter(process.stdout.readline, ''):
                    if not line:
                        break
                    yield line.rstrip('\n')

            # 等待进程结束
            process.wait(timeout=timeout or self.timeout)

            # 如果有错误输出，也返回
            if process.stderr:
                for line in process.stderr:
                    stripped_line = line.rstrip('\n')
                    yield f"[STDERR] {stripped_line}"

        except subprocess.TimeoutExpired:
            if process:
                process.kill()
            yield f"[ERROR] Command timeout after {timeout or self.timeout} seconds"
        except Exception as e:
            yield f"[ERROR] Execution error: {str(e)}"
