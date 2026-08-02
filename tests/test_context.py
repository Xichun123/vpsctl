from datetime import datetime, timezone
from pathlib import Path
import tempfile
import unittest
from unittest import mock

from vpsctl.context import build_context, safe_host_profile, snapshot_context
from vpsctl.store import Store


class ContextTests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.store = Store(Path(self.temp_dir.name) / "state.db")
        self.now = datetime(2026, 1, 1, 0, 10, tzinfo=timezone.utc)

    def tearDown(self):
        self.temp_dir.cleanup()

    def test_freshness_transitions_and_failed_refresh_fallback(self):
        self.store.save_snapshot(
            "host", "web", "web", True, {"hostname": "web-01"},
            observed_at="2026-01-01T00:09:00+00:00",
        )
        fresh = snapshot_context(self.store, "host", "web", 300, now=self.now)
        stale = snapshot_context(self.store, "host", "web", 30, now=self.now)
        self.assertEqual(fresh["freshness"]["status"], "fresh")
        self.assertEqual(stale["freshness"]["status"], "stale")

        self.store.save_snapshot(
            "host", "web", "web", False, error="timeout",
            observed_at="2026-01-01T00:10:00+00:00",
        )
        failed = snapshot_context(self.store, "host", "web", 300, now=self.now)
        self.assertEqual(failed["freshness"]["status"], "refresh_failed")
        self.assertEqual(failed["observation"]["hostname"], "web-01")
        self.assertEqual(failed["freshness"]["last_error"], "timeout")

    def test_safe_host_profile_redacts_password(self):
        ssh_dir = Path(self.temp_dir.name) / ".ssh"
        ssh_dir.mkdir()
        (ssh_dir / "config").write_text(
            "# password: super-secret\n"
            "# description: demo host\n"
            "Host web\n"
            "    HostName 192.0.2.10\n"
            "    User deploy\n",
            encoding="utf-8",
        )
        with mock.patch.dict("os.environ", {"HOME": self.temp_dir.name}):
            profile = safe_host_profile("web")

        self.assertEqual(profile["auth"], "password")
        self.assertNotIn("password", profile)
        self.assertNotIn("super-secret", str(profile))

    def test_no_ttl_uses_tracked_status(self):
        self.store.save_snapshot(
            "host", "web", "web", True, {"hostname": "web-01"},
            observed_at="2020-01-01T00:00:00+00:00",
        )
        result = snapshot_context(self.store, "host", "web", None, now=self.now)
        self.assertEqual(result["freshness"]["status"], "tracked")

    def test_project_context_combines_static_and_observed_data(self):
        self.store.add_project(
            "app", host_alias="web", remote_path="/opt/app",
            deploy_command="git pull && docker compose up -d",
            protected_paths=[".env"],
        )
        observed_at = "2026-01-01T00:09:30+00:00"
        self.store.save_snapshot(
            "host", "web", "web", True,
            {
                "hostname": "web-01",
                "listening_sockets": ["secretly-large-list"],
                "docker": {"available": True, "version": "29", "containers": [{"Names": "app"}]},
            },
            observed_at=observed_at,
        )
        self.store.save_snapshot(
            "project", "app", "web", True,
            {"git": {"branch": "main", "commit": "abc"}}, observed_at=observed_at,
        )
        host_profile = {
            "alias": "web", "hostname": "192.0.2.10", "auth": "key"
        }
        with mock.patch("vpsctl.context.safe_host_profile", return_value=host_profile):
            result = build_context(
                self.store, project_name="app", max_age_seconds=300, now=self.now
            )

        self.assertTrue(result["success"])
        self.assertTrue(result["complete"])
        self.assertEqual(result["host"]["profile"]["auth"], "key")
        self.assertEqual(result["mode"], "compact")
        self.assertNotIn("listening_sockets", result["host"]["observation"])
        self.assertEqual(result["host"]["observation"]["docker"]["container_count"], 1)
        self.assertEqual(result["projects"][0]["profile"]["protected_paths"], [".env"])
        self.assertEqual(
            result["projects"][0]["observation"]["git"]["branch"], "main"
        )


if __name__ == "__main__":
    unittest.main()
