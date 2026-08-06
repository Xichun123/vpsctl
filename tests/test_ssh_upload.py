import io
import os
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from types import SimpleNamespace
from unittest import mock

from vpsctl._runtime import ssh_upload
from vpsctl._runtime.lib.native_ssh_client import NativeSSHClient


class NativeUploadTimeoutTests(unittest.TestCase):
    def setUp(self):
        handle = tempfile.NamedTemporaryFile(delete=False)
        handle.write(b"payload")
        handle.close()
        self.local_path = handle.name
        self.client = NativeSSHClient(
            host="example.test",
            user="deploy",
            key_file="~/.ssh/id_ed25519",
            timeout=30,
        )

    def tearDown(self):
        os.unlink(self.local_path)

    @mock.patch("vpsctl._runtime.lib.native_ssh_client.subprocess.run")
    def test_upload_has_no_total_timeout_by_default(self, run):
        run.return_value = subprocess.CompletedProcess([], 0, "", "")

        result = self.client.upload(self.local_path, "/tmp/app")

        self.assertTrue(result.success)
        self.assertIsNone(run.call_args.kwargs["timeout"])
        command = run.call_args.args[0]
        self.assertIn("ConnectTimeout=30", command)

    @mock.patch("vpsctl._runtime.lib.native_ssh_client.subprocess.run")
    def test_upload_uses_and_reports_explicit_timeout(self, run):
        run.side_effect = subprocess.TimeoutExpired(cmd=["scp"], timeout=180)

        result = self.client.upload(self.local_path, "/tmp/app", timeout=180)

        self.assertFalse(result.success)
        self.assertEqual(run.call_args.kwargs["timeout"], 180)
        self.assertEqual(result.stderr, "Upload timeout after 180 seconds")


class UploadCLITests(unittest.TestCase):
    def test_native_upload_receives_explicit_timeout(self):
        handle = tempfile.NamedTemporaryFile(delete=False)
        handle.write(b"payload")
        handle.close()

        client = mock.Mock()
        client.upload.return_value = SimpleNamespace(
            success=True,
            stdout="uploaded",
            stderr="",
            exit_code=0,
        )
        loader = mock.Mock()
        loader.get_connection_params.return_value = {
            "key_file": "~/.ssh/id_ed25519",
            "password": None,
        }
        loader.from_alias.return_value = client
        config_module = SimpleNamespace(SSHConfigLoaderV3=mock.Mock(return_value=loader))

        try:
            argv = [
                "ssh_upload.py",
                "demo",
                handle.name,
                "/tmp/app",
                "--timeout",
                "180",
                "--no-progress",
            ]
            with mock.patch.object(sys, "argv", argv), \
                    mock.patch.dict(sys.modules, {"config_v3": config_module}), \
                    redirect_stdout(io.StringIO()), \
                    self.assertRaises(SystemExit) as exit_context:
                ssh_upload.main()
        finally:
            os.unlink(handle.name)

        self.assertEqual(exit_context.exception.code, 0)
        client.upload.assert_called_once_with(
            os.path.abspath(handle.name),
            "/tmp/app",
            timeout=180,
            show_progress=False,
        )

    def test_timeout_must_be_positive(self):
        with self.assertRaises(Exception) as context:
            ssh_upload._positive_timeout("0")

        self.assertIn("greater than 0", str(context.exception))


if __name__ == "__main__":
    unittest.main()
