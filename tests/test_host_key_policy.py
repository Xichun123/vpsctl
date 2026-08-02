from pathlib import Path
import tempfile
import unittest
from unittest import mock

import paramiko

from vpsctl._runtime.lib.host_key_policy import configure_paramiko_host_keys
from vpsctl._runtime.lib.native_ssh_client import NativeSSHClient
from vpsctl._runtime.lib.paramiko_client import ParamikoClient


class OpenSSHHostKeyPolicyTests(unittest.TestCase):
    def assert_accept_new_policy(self, args):
        self.assertIn("StrictHostKeyChecking=accept-new", args)
        self.assertNotIn("StrictHostKeyChecking=no", args)
        self.assertFalse(
            any(arg == "UserKnownHostsFile=/dev/null" for arg in args),
            args,
        )

    def test_native_ssh_command_persists_and_checks_host_keys(self):
        client = NativeSSHClient(
            host="192.0.2.10",
            user="root",
            key_file="~/.ssh/demo",
        )

        self.assert_accept_new_policy(client._build_ssh_base_args())

    def test_paramiko_scp_fallback_persists_and_checks_host_keys(self):
        client = ParamikoClient(
            host="192.0.2.10",
            user="root",
            key_file="~/.ssh/demo",
        )

        args = client._build_scp_command("source", "/tmp/destination")

        self.assert_accept_new_policy(args)

    def test_runtime_never_disables_openssh_host_key_checking(self):
        runtime_dir = Path(__file__).resolve().parents[1] / "src/vpsctl/_runtime"
        insecure_fragments = (
            "StrictHostKeyChecking=no",
            "UserKnownHostsFile=/dev/null",
        )
        violations = []

        for path in runtime_dir.rglob("*.py"):
            source = path.read_text(encoding="utf-8")
            for fragment in insecure_fragments:
                if fragment in source:
                    violations.append(f"{path.relative_to(runtime_dir)}: {fragment}")

        self.assertEqual(violations, [])


class ParamikoHostKeyPolicyTests(unittest.TestCase):
    def test_paramiko_uses_a_persistent_known_hosts_file(self):
        client = mock.Mock()

        with tempfile.TemporaryDirectory() as temp_dir:
            known_hosts = Path(temp_dir) / ".ssh/known_hosts"

            configured_path = configure_paramiko_host_keys(client, known_hosts)

            self.assertEqual(configured_path, known_hosts)
            self.assertTrue(known_hosts.is_file())
            self.assertEqual(known_hosts.stat().st_mode & 0o777, 0o600)
            client.load_system_host_keys.assert_called_once_with()
            client.load_host_keys.assert_called_once_with(str(known_hosts))
            policy = client.set_missing_host_key_policy.call_args.args[0]
            self.assertIsInstance(policy, paramiko.AutoAddPolicy)


if __name__ == "__main__":
    unittest.main()
