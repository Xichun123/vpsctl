import os
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

from vpsctl._runtime import ssh_execute


class ResolveCommandTests(unittest.TestCase):
    def test_resolve_plain_command(self):
        spec = ssh_execute._resolve_command(
            SimpleNamespace(command="echo ok", stdin=False, script_file=None)
        )

        self.assertEqual(spec, {"mode": "command", "command": "echo ok"})

    def test_resolve_script_file_honors_shebang(self):
        with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as handle:
            handle.write("#!/usr/bin/env bash\nset -e\n")
            script_path = handle.name

        try:
            spec = ssh_execute._resolve_command(
                SimpleNamespace(command=None, stdin=False, script_file=script_path)
            )
        finally:
            os.unlink(script_path)

        self.assertEqual(spec["mode"], "script")
        self.assertEqual(spec["runner"], ["/usr/bin/env", "bash"])
        self.assertIn("set -e", spec["script_text"])

    def test_resolve_script_runner_supports_env_split_mode(self):
        runner = ssh_execute._resolve_script_runner(
            "#!/usr/bin/env -S bash -eu\nexit 0\n"
        )

        self.assertEqual(runner, ["/usr/bin/env", "-S", "bash -eu"])


class DirectExecuteTests(unittest.TestCase):
    def test_direct_execute_uses_client_script_api(self):
        fake_client = mock.Mock()
        fake_client.execute_script.return_value = SimpleNamespace(
            success=True,
            exit_code=0,
            stdout="ok\n",
            stderr="",
        )

        fake_loader = mock.Mock()
        fake_loader.load_ssh_config.return_value = {}
        fake_loader.load_metadata.return_value = {}
        fake_loader.from_alias.return_value = fake_client

        spec = {
            "mode": "script",
            "script_text": "#!/usr/bin/env bash\necho ok\n",
            "runner": ["/usr/bin/env", "bash"],
        }

        with mock.patch("config_v3.SSHConfigLoaderV3", return_value=fake_loader), \
                mock.patch("native_ssh_fallback.should_use_native_ssh", return_value=(False, "")):
            result = ssh_execute.direct_execute("demo-host", spec, 42)

        fake_client.execute_script.assert_called_once_with(
            spec["script_text"],
            runner=spec["runner"],
            timeout=42,
        )
        self.assertEqual(result["stdout"], "ok\n")

    def test_direct_execute_uses_native_script_fallback(self):
        fake_loader = mock.Mock()
        fake_loader.load_ssh_config.return_value = {}
        fake_loader.load_metadata.return_value = {}

        spec = {
            "mode": "script",
            "script_text": "#!/bin/sh\necho ok\n",
            "runner": ["/bin/sh"],
        }

        with mock.patch("config_v3.SSHConfigLoaderV3", return_value=fake_loader), \
                mock.patch("native_ssh_fallback.should_use_native_ssh", return_value=(True, "proxy")), \
                mock.patch("native_ssh_fallback.check_ssh_agent", return_value=(True, "ok")), \
                mock.patch(
                    "native_ssh_fallback.execute_native_ssh_script",
                    return_value={"success": True, "exit_code": 0, "stdout": "ok\n", "stderr": ""},
                ) as execute_native_ssh_script:
            result = ssh_execute.direct_execute("demo-host", spec, 30)

        execute_native_ssh_script.assert_called_once_with(
            "demo-host",
            spec["script_text"],
            runner=spec["runner"],
            timeout=30,
        )
        self.assertEqual(result["fallback_reason"], "proxy")


if __name__ == "__main__":
    unittest.main()
