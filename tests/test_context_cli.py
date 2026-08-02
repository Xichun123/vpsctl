import io
import json
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

from vpsctl import context_cli, discovery
from vpsctl.store import Store


class RoutingExecutor:
    def execute_script(self, alias, script, timeout=60):
        marker = discovery._MARKER
        if script == discovery.HOST_SCRIPT:
            stdout = f"{marker}hostname\nweb-01\n{marker}cpu_count\n2\n"
        else:
            stdout = (
                f"{marker}path\n/opt/app\n"
                f"{marker}path_exists\ntrue\n"
                f"{marker}git_branch\nmain\n"
                f"{marker}git_commit\nabc123\n"
            )
        return {"success": True, "stdout": stdout, "stderr": "", "exit_code": 0}


class ContextCLITests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.store = Store(Path(self.temp_dir.name) / "state.db")
        self.store.add_project("app", host_alias="web", remote_path="/opt/app")

    def tearDown(self):
        self.temp_dir.cleanup()

    def test_routine_context_never_calls_remote_executor(self):
        self.store.save_snapshot("host", "web", "web", True, {"hostname": "cached"})
        self.store.save_snapshot(
            "project", "app", "web", True, {"path": "/opt/app", "path_exists": True}
        )
        executor = mock.Mock()
        stdout = io.StringIO()
        profile = {"alias": "web", "hostname": "192.0.2.10", "auth": "key"}
        with mock.patch("vpsctl.context.safe_host_profile", return_value=profile), \
                redirect_stdout(stdout):
            code = context_cli.context_main(
                ["--project", "app"], store=self.store, executor=executor
            )

        self.assertEqual(code, 0)
        executor.execute_script.assert_not_called()
        result = json.loads(stdout.getvalue())
        self.assertEqual(result["mode"], "compact")
        self.assertEqual(result["host"]["freshness"]["status"], "tracked")

    def test_context_refreshes_host_and_project(self):
        stdout = io.StringIO()
        profile = {"alias": "web", "hostname": "192.0.2.10", "auth": "key"}
        with mock.patch("vpsctl.context.safe_host_profile", return_value=profile), \
                redirect_stdout(stdout):
            code = context_cli.context_main(
                ["--project", "app", "--refresh"],
                store=self.store,
                executor=RoutingExecutor(),
            )

        result = json.loads(stdout.getvalue())
        self.assertEqual(code, 0)
        self.assertTrue(result["refresh"]["success"])
        self.assertEqual(result["host"]["observation"]["hostname"], "web-01")
        self.assertEqual(
            result["projects"][0]["observation"]["git"]["commit"], "abc123"
        )

    def test_refresh_failure_returns_cached_context_and_nonzero(self):
        self.store.save_snapshot(
            "host", "web", "web", True, {"hostname": "cached"}
        )
        failing = mock.Mock()
        failing.execute_script.side_effect = discovery.DiscoveryError("offline")
        stdout = io.StringIO()
        profile = {"alias": "web", "hostname": "192.0.2.10", "auth": "key"}
        with mock.patch("vpsctl.context.safe_host_profile", return_value=profile), \
                redirect_stdout(stdout):
            code = context_cli.context_main(
                ["--host", "web", "--refresh"],
                store=self.store,
                executor=failing,
            )

        result = json.loads(stdout.getvalue())
        self.assertEqual(code, 1)
        self.assertFalse(result["success"])
        self.assertEqual(result["host"]["observation"]["hostname"], "cached")
        self.assertEqual(result["host"]["freshness"]["status"], "refresh_failed")


if __name__ == "__main__":
    unittest.main()
