import os
from pathlib import Path
import tempfile
import unittest

from vpsctl.store import Store, StoreError


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.db_path = Path(self.temp_dir.name) / "state.db"
        self.store = Store(self.db_path)

    def tearDown(self):
        self.temp_dir.cleanup()

    def test_project_crud_and_list_normalization(self):
        project = self.store.add_project(
            "my-app",
            host_alias="prod-web",
            remote_path="/opt/my-app",
            runtime="docker-compose",
            tags=["web", "web", " production "],
            domains=["app.example.com"],
        )

        self.assertEqual(project["tags"], ["web", "production"])
        self.assertEqual(
            self.store.list_projects(host_alias="prod-web", tag="web")[0]["name"],
            "my-app",
        )

        updated = self.store.update_project(
            "my-app",
            {"service": "my-app.service", "tags": ["api"]},
            clear_fields=["domains"],
        )
        self.assertEqual(updated["service"], "my-app.service")
        self.assertEqual(updated["tags"], ["api"])
        self.assertEqual(updated["domains"], [])

        self.store.delete_project("my-app")
        with self.assertRaises(StoreError):
            self.store.get_project("my-app")

    def test_project_requires_absolute_remote_path(self):
        with self.assertRaisesRegex(StoreError, "绝对路径"):
            self.store.add_project(
                "bad", host_alias="prod-web", remote_path="relative/path"
            )

    def test_duplicate_project_is_rejected(self):
        self.store.add_project("app", host_alias="web", remote_path="/opt/app")
        with self.assertRaisesRegex(StoreError, "已存在"):
            self.store.add_project("app", host_alias="web", remote_path="/srv/app")

    def test_snapshots_keep_latest_success_and_failure(self):
        self.store.save_snapshot(
            "host", "prod-web", "prod-web", True, {"hostname": "web-1"},
            observed_at="2026-01-01T00:00:00+00:00",
        )
        self.store.save_snapshot(
            "host", "prod-web", "prod-web", False, error="timeout",
            observed_at="2026-01-01T00:01:00+00:00",
        )

        latest = self.store.latest_snapshot("host", "prod-web")
        successful = self.store.latest_snapshot("host", "prod-web", successful_only=True)
        self.assertFalse(latest["success"])
        self.assertEqual(successful["data"]["hostname"], "web-1")

    def test_change_journal_is_project_scoped(self):
        self.store.add_project("app", host_alias="web", remote_path="/opt/app")
        change = self.store.add_change(
            "app",
            kind="deploy",
            summary="部署版本 2",
            details="容器已重建",
            operation="command",
            payload_sha256="a" * 64,
            success=True,
            exit_code=0,
        )

        self.assertEqual(change["host_alias"], "web")
        self.assertEqual(change["summary"], "部署版本 2")
        self.assertEqual(
            self.store.list_changes(project_name="app")[0]["payload_sha256"],
            "a" * 64,
        )

        self.store.delete_project("app")
        self.assertEqual(self.store.list_changes(host_alias="web"), [])

    @unittest.skipIf(os.name == "nt", "POSIX permissions only")
    def test_database_is_private(self):
        self.assertEqual(self.db_path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.db_path.parent.stat().st_mode & 0o777, 0o700)


if __name__ == "__main__":
    unittest.main()
