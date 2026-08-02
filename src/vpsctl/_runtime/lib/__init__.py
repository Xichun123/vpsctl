"""
SSH Skill library exports.

Expose the current v3 modules so `import lib` works without referencing
stale pre-v3 files.
"""

from .cluster import SSHCluster
from .config_v3 import SSHConfigLoaderV3, get_config_loader_v3
from .native_ssh_client import NativeSSHClient, SSHResult
from .paramiko_client import ParamikoClient
from .utils import check_ssh_available, get_ssh_version, validate_key_file

__version__ = "3.3.0"

__all__ = [
    "SSHCluster",
    "SSHConfigLoaderV3",
    "get_config_loader_v3",
    "NativeSSHClient",
    "ParamikoClient",
    "SSHResult",
    "check_ssh_available",
    "get_ssh_version",
    "validate_key_file",
]
