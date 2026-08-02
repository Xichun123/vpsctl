"""Shared host-key verification policy for SSH clients."""

import os
from pathlib import Path
from typing import Optional, Union


OPENSSH_ACCEPT_NEW_ARGS = ("-o", "StrictHostKeyChecking=accept-new")


def openssh_accept_new_args():
    """Return mutable OpenSSH arguments that persist new host keys."""
    return list(OPENSSH_ACCEPT_NEW_ARGS)


def configure_paramiko_host_keys(client, known_hosts_path: Optional[Union[str, Path]] = None):
    """Give Paramiko OpenSSH-like accept-new behavior with persistent keys."""
    import paramiko

    path = Path(known_hosts_path or "~/.ssh/known_hosts").expanduser()
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)

    if not path.exists():
        descriptor = os.open(path, os.O_CREAT | os.O_WRONLY, 0o600)
        os.close(descriptor)

    client.load_system_host_keys()
    client.load_host_keys(str(path))
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    return path
