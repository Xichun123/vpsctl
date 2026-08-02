from pathlib import Path
import tempfile
import unittest

from vpsctl import discovery
from vpsctl.store import Store


class FakeExecutor:
    def __init__(self, stdout="", error=None):
        self.stdout = stdout
        self.error = error
        self.calls = []

    def execute_script(self, alias, script, timeout=60):
        self.calls.append((alias, script, timeout))
        if self.error:
            raise discovery.DiscoveryError(self.error)
        return {"success": True, "stdout": self.stdout, "stderr": "", "exit_code": 0}


class DiscoveryTests(unittest.TestCase):
    def test_parse_and_structure_host_discovery(self):
        marker = discovery._MARKER
        output = f"""{marker}hostname
web-01
{marker}cpu_count
4
{marker}os_release
NAME=Ubuntu
PRETTY_NAME=\"Ubuntu 24.04 LTS\"
{marker}memory
MemTotal:       8192000 kB
MemAvailable:   4096000 kB
{marker}disk_root
/dev/vda1 100000 40000 60000 40% /
{marker}addresses
10.0.0.2 2001:db8::1
{marker}docker_version
27.0.1
{marker}docker_containers
{{\"Names\":\"app\",\"State\":\"running\"}}
"""
        result = discovery.discover_host("web", FakeExecutor(output))

        self.assertEqual(result["hostname"], "web-01")
        self.assertEqual(result["cpu_count"], 4)
        self.assertEqual(result["os"]["pretty_name"], "Ubuntu 24.04 LTS")
        self.assertEqual(result["memory"]["memtotal_kib"], 8192000)
        self.assertEqual(result["disk_root"]["used_percent"], "40%")
        self.assertTrue(result["docker"]["available"])
        self.assertEqual(result["docker"]["containers"][0]["Names"], "app")

    def test_project_script_shell_quotes_profile_values(self):
        project = {
            "remote_path": "/opt/app; touch /tmp/pwned",
            "service": "app'; reboot; echo '",
            "compose_file": "compose.prod.yml",
        }
        script = discovery.build_project_script(project)

        self.assertIn("project_path='/opt/app; touch /tmp/pwned'", script)
        self.assertIn("configured_compose=compose.prod.yml", script)
        self.assertNotIn("project_path=/opt/app; touch", script)

    def test_failed_refresh_is_saved_without_losing_success(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            store = Store(Path(temp_dir) / "state.db")
            store.save_snapshot("host", "web", "web", True, {"hostname": "old"})
            result = discovery.refresh_host(store, "web", FakeExecutor(error="timeout"))

            self.assertFalse(result["success"])
            self.assertEqual(
                store.latest_snapshot("host", "web", successful_only=True)["data"]["hostname"],
                "old",
            )


if __name__ == "__main__":
    unittest.main()
