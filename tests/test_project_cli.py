import io
import json
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout

from vpsctl import project_cli
from vpsctl.store import Store


class ProjectCLITests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.store = Store(Path(self.temp_dir.name) / "state.db")

    def tearDown(self):
        self.temp_dir.cleanup()

    def run_cli(self, args):
        stdout = io.StringIO()
        stderr = io.StringIO()
        with redirect_stdout(stdout), redirect_stderr(stderr):
            code = project_cli.main(args, store=self.store)
        output_text = stdout.getvalue().strip()
        error_text = stderr.getvalue().strip()
        output = json.loads(output_text) if output_text else None
        # unittest 的 verbose runner 在部分 Python 版本中也会写入当前 stderr；
        # CLI 错误对象从首个 JSON 花括号开始解析。
        json_start = error_text.find("{")
        error = json.loads(error_text[json_start:]) if json_start >= 0 else None
        return code, output, error

    def test_add_show_update_list_and_delete(self):
        code, output, _ = self.run_cli([
            "add", "my-app", "--host", "prod-web", "--path", "/opt/my-app",
            "--runtime", "docker-compose", "--tag", "web",
        ])
        self.assertEqual(code, 0)
        self.assertEqual(output["project"]["host_alias"], "prod-web")

        code, output, _ = self.run_cli(["show", "my-app"])
        self.assertEqual(code, 0)
        self.assertEqual(output["project"]["remote_path"], "/opt/my-app")

        code, output, _ = self.run_cli([
            "update", "my-app", "--service", "my-app.service", "--domain", "app.example.com",
        ])
        self.assertEqual(output["project"]["service"], "my-app.service")

        code, output, _ = self.run_cli(["list", "--tag", "web"])
        self.assertEqual(output["count"], 1)

        code, output, _ = self.run_cli(["delete", "my-app"])
        self.assertEqual(code, 0)
        self.assertIn("已删除", output["message"])

    def test_errors_are_json(self):
        code, output, error = self.run_cli(["show", "missing"])
        self.assertEqual(code, 1)
        self.assertIsNone(output)
        self.assertFalse(error["success"])


if __name__ == "__main__":
    unittest.main()
