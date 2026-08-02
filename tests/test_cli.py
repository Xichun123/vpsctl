import io
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

from vpsctl import cli


class ResolveCommandTests(unittest.TestCase):
    def test_short_list_maps_to_host_list(self):
        self.assertEqual(
            cli.resolve_command(["list", "--environment", "production"]),
            (
                "ssh_config_manager_v3.py",
                ["list-servers", "--environment", "production"],
            ),
        )

    def test_exec_preserves_complex_arguments(self):
        self.assertEqual(
            cli.resolve_command(["exec", "web-01", "echo $HOME", "--no-daemon"]),
            ("ssh_execute.py", ["web-01", "echo $HOME", "--no-daemon"]),
        )

    def test_host_add_is_create_alias(self):
        self.assertEqual(
            cli.resolve_command(["host", "add", "--alias", "web-01"]),
            ("ssh_config_manager_v3.py", ["create", "--alias", "web-01"]),
        )

    def test_nested_commands_map_to_runtime(self):
        cases = {
            ("key", "verify"): ("ssh_key_manager.py", ["verify"]),
            ("key", "deploy"): ("deploy_pubkey.py", []),
            ("config", "migrate"): ("migrate_to_ssh_config.py", []),
            ("inventory", "refresh"): ("update_server_info.py", []),
        }
        for command, expected in cases.items():
            with self.subTest(command=command):
                self.assertEqual(cli.resolve_command(list(command)), expected)

    def test_unknown_command_is_rejected(self):
        with self.assertRaises(cli.CLIError):
            cli.resolve_command(["destroy-everything"])


class MainTests(unittest.TestCase):
    def test_help_does_not_start_runtime(self):
        stdout = io.StringIO()
        with mock.patch.object(cli, "run_runtime") as run_runtime, redirect_stdout(stdout):
            code = cli.main(["--help"])

        self.assertEqual(code, 0)
        self.assertIn("vpsctl", stdout.getvalue())
        run_runtime.assert_not_called()

    def test_dangerous_helper_help_does_not_run_helper(self):
        stdout = io.StringIO()
        with mock.patch.object(cli, "run_runtime") as run_runtime, redirect_stdout(stdout):
            code = cli.main(["config", "annotate", "--help"])

        self.assertEqual(code, 0)
        self.assertIn("会写入配置文件", stdout.getvalue())
        run_runtime.assert_not_called()

    def test_dispatches_resolved_command(self):
        with mock.patch.object(cli, "run_runtime", return_value=17) as run_runtime:
            code = cli.main(["tunnel", "list"])

        self.assertEqual(code, 17)
        run_runtime.assert_called_once_with("ssh_tunnel.py", ["list"])

    def test_dispatches_native_project_command(self):
        with mock.patch("vpsctl.project_cli.main", return_value=0) as project_main:
            code = cli.main(["project", "list", "--host", "web"])

        self.assertEqual(code, 0)
        project_main.assert_called_once_with(["list", "--host", "web"])

    def test_dispatches_native_context_command(self):
        with mock.patch("vpsctl.context_cli.context_main", return_value=0) as context_main:
            code = cli.main(["context", "--project", "app"])

        self.assertEqual(code, 0)
        context_main.assert_called_once_with(["--project", "app"])

    def test_dispatches_apply_command(self):
        with mock.patch("vpsctl.apply_cli.apply_main", return_value=0) as apply_main:
            code = cli.main(["apply", "app", "echo ok", "--summary", "更新"])

        self.assertEqual(code, 0)
        apply_main.assert_called_once_with(["app", "echo ok", "--summary", "更新"])

    def test_unknown_command_returns_usage_error(self):
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            code = cli.main(["unknown"])

        self.assertEqual(code, 2)
        self.assertIn("未知命令", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
