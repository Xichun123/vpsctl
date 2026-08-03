import unittest

from vpsctl._runtime.lib.utils import omit_empty_stderr


class OmitEmptyStderrTests(unittest.TestCase):
    def test_omits_empty_stderr_recursively(self):
        result = omit_empty_stderr(
            {
                "success": True,
                "stderr": "",
                "results": {
                    "web-01": {"stdout": "ok\n", "stderr": None},
                    "web-02": {"stdout": "ok\n", "stderr": "   \n"},
                },
            }
        )

        self.assertNotIn("stderr", result)
        self.assertNotIn("stderr", result["results"]["web-01"])
        self.assertNotIn("stderr", result["results"]["web-02"])

    def test_preserves_nonempty_stderr_and_other_empty_fields(self):
        source = {
            "success": True,
            "stdout": "",
            "stderr": "warning: deprecated option\n",
        }

        result = omit_empty_stderr(source)

        self.assertEqual(result["stderr"], "warning: deprecated option\n")
        self.assertEqual(result["stdout"], "")
        self.assertEqual(source["stderr"], "warning: deprecated option\n")


if __name__ == "__main__":
    unittest.main()
