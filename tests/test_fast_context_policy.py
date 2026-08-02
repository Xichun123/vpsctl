from pathlib import Path
import unittest

from vpsctl.context_cli import build_context_parser


class FastContextPolicyTests(unittest.TestCase):
    skill_path = Path(__file__).parents[1] / "skills" / "vpsctl" / "SKILL.md"

    def test_routine_context_is_local_compact_and_has_no_ttl(self):
        args = build_context_parser().parse_args(["--project", "app"])

        self.assertFalse(args.refresh)
        self.assertIsNone(args.max_age)
        self.assertTrue(args.compact)

    def test_skill_forces_new_project_profile_without_ordering(self):
        text = self.skill_path.read_text(encoding="utf-8")

        self.assertIn("must end with a vpsctl project profile", text)
        self.assertIn("may happen in either order", text)
        self.assertIn("vpsctl project show <name>", text)
        self.assertIn("must succeed", text)

    def test_skill_routine_workflow_does_not_force_refresh(self):
        text = self.skill_path.read_text(encoding="utf-8")
        workflow = text.split("## Project workflow", 1)[1].split("## New project rule", 1)[0]

        self.assertIn("vpsctl context --project <name>", workflow)
        self.assertNotIn("vpsctl context --project <name> --refresh", workflow)


if __name__ == "__main__":
    unittest.main()
