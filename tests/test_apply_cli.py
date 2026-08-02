import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stdout
from types import SimpleNamespace
from unittest import mock

from vpsctl import apply_cli
from vpsctl.store import Store


class ApplyCLITests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.store = Store(Path(self.temp_dir.name) / "state.db")
        self.store.add_project("app", host_alias="dm", remote_path="/opt/app")

    def tearDown(self):
        self.temp_dir.cleanup()

    def test_successful_apply_executes_project_host_and_records_hash(self):
        runtime_result = {
            "success": True,
            "exit_code": 0,
            "stdout": "done\n",
            "stderr": "",
        }
        completed = SimpleNamespace(
            returncode=0, stdout=json.dumps(runtime_result), stderr=""
        )
        stdout = io.StringIO()
        command = "docker compose up -d"

        with mock.patch("vpsctl.apply_cli.subprocess.run", return_value=completed) as run, \
                redirect_stdout(stdout):
            code = apply_cli.apply_main(
                [
                    "app", "--summary", "部署新版本", "--kind", "deploy", command,
                ],
                store=self.store,
                runtime_dir=Path("/runtime"),
            )

        result = json.loads(stdout.getvalue())
        self.assertEqual(code, 0)
        self.assertEqual(result["change"]["summary"], "部署新版本")
        self.assertEqual(
            result["change"]["payload_sha256"],
            hashlib.sha256(command.encode()).hexdigest(),
        )
        self.assertNotIn(command, json.dumps(result["change"], ensure_ascii=False))
        invoked = run.call_args.args[0]
        self.assertEqual(invoked[2], "dm")
        self.assertIn(command, invoked)

    def test_failed_apply_is_also_recorded(self):
        runtime_result = {
            "success": False,
            "exit_code": 7,
            "stdout": "",
            "stderr": "failed",
        }
        completed = SimpleNamespace(
            returncode=1, stdout=json.dumps(runtime_result), stderr=""
        )
        stdout = io.StringIO()

        with mock.patch("vpsctl.apply_cli.subprocess.run", return_value=completed), \
                redirect_stdout(stdout):
            code = apply_cli.apply_main(
                ["app", "false", "--summary", "尝试更新配置", "--kind", "config"],
                store=self.store,
            )

        self.assertEqual(code, 1)
        changes = self.store.list_changes(project_name="app")
        self.assertEqual(len(changes), 1)
        self.assertFalse(changes[0]["success"])
        self.assertEqual(changes[0]["exit_code"], 7)


if __name__ == "__main__":
    unittest.main()
