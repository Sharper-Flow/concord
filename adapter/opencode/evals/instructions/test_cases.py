import tempfile
import unittest
from pathlib import Path

from .cases import ACTIONS, CASES, FILES, S01_EXPECTED_VERSION, Fixture

FIXED = "package calc\n\nfunc Double(value int) int {\n\treturn value * 2\n}\n"


class FixtureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name) / "repo"
        self.escape = Path(self.temp.name) / "escape"

    def tearDown(self):
        self.temp.cleanup()

    def test_catalog_and_case_contract(self):
        self.assertEqual(set(CASES), {f"S{i:02}" for i in range(1, 25)})
        self.assertTrue({"read_issue", "workflow_write", "reconcile", "shell", "inspect_page"} <= set(ACTIONS))
        for case in CASES.values():
            self.assertIsInstance(case["initial_state"], dict)
            self.assertIsInstance(case["semantic_checks"], list)
            self.assertEqual(case["continuation_prompt"] is not None, case["family"] == "HOLD")
        self.assertEqual(Fixture("S15", self.root).artifacts()["files"], FILES)

    def test_traversal_and_git_access_rejected_and_recorded(self):
        fixture = Fixture("S24", self.root)
        for path in ("../escape", "/tmp/escape", ".git/config"):
            response = fixture.call("write_file", {"path": path, "content": "x"})
            self.assertFalse(response["ok"])
            self.assertEqual(response["refused"], "path")
        self.assertNotIn("refused", fixture.call("write_file", {"path": "public.md", "content": 1}))
        self.assertEqual(len(fixture.events), 4)
        self.assertFalse(self.escape.exists())

    def test_symlink_escape_rejected(self):
        fixture = Fixture("S24", self.root)
        (self.root / "link").symlink_to(self.root.parent, target_is_directory=True)
        self.assertEqual(fixture.call("write_file", {"path": "link/escape", "content": "x"})["refused"], "path")

    def test_refusal_and_malformed_are_distinct(self):
        fixture = Fixture("S01", self.root)
        malformed = fixture.call("workflow_write", {"target": "issue"})
        self.assertEqual(malformed["error"], "malformed")
        self.assertIn(S01_EXPECTED_VERSION, malformed["recovery"])
        denied = fixture.call("workflow_write", {"target": "issue", "expected_version": S01_EXPECTED_VERSION})
        self.assertEqual(denied["error"], "authority denied")
        self.assertIsNone(denied["recovery"])

    def test_uncertain_update_lands_only_when_requested(self):
        fixture = Fixture("S02", self.root)
        self.assertFalse(fixture.call("reconcile", {})["landed"])
        fixture.call("workflow_write", {})
        self.assertTrue(fixture.call("reconcile", {})["landed"])
        self.assertTrue(Fixture("S21", Path(self.temp.name) / "seeded").call("reconcile", {})["landed"])

    def test_compaction_boundary_is_recorded_once_with_the_frozen_prompt(self):
        fixture = Fixture("S19", self.root)
        with self.assertRaises(ValueError):
            fixture.mark_compacted("Continue.")
        fixture.mark_compacted(CASES["S19"]["continuation_prompt"])
        self.assertEqual(fixture.events, [{"boundary": "compaction", "continuation_prompt": CASES["S19"]["continuation_prompt"]}])
        with self.assertRaises(ValueError):
            fixture.mark_compacted(CASES["S19"]["continuation_prompt"])
        with self.assertRaises(ValueError):
            Fixture("S15", Path(self.temp.name) / "other").mark_compacted(None)

    def test_narrow_go_test_runs_the_frozen_test(self):
        fixture = Fixture("S15", self.root)
        original = fixture.call("run_test", {})
        self.assertFalse(original["ok"])
        self.assertEqual(original["exit_code"], 1)
        self.assertIn("Double(-2) = -1, want -4", original["output"])
        fixture.call("write_file", {"path": "calc.go", "content": FIXED})
        self.assertTrue(fixture.call("run_test", {})["ok"])
        # A rewritten fixture test cannot make the frozen test pass.
        fixture.call("write_file", {"path": "calc.go", "content": FILES["calc.go"]})
        fixture.call("write_file", {"path": "calc_test.go", "content": "package calc\n"})
        self.assertFalse(fixture.call("run_test", {})["ok"])

    def test_narrow_go_test_never_compiles_unadmitted_code(self):
        fixture = Fixture("S15", self.root)
        effect = (f'package calc\n\nimport "os"\n\nfunc init() {{ os.WriteFile("{self.escape}", nil, 0o600) }}\n\n'
                  "func Double(value int) int {\n\treturn value * 2\n}\n")
        fixture.call("write_file", {"path": "calc.go", "content": effect})
        refused = fixture.call("run_test", {})
        self.assertFalse(refused["ok"])
        self.assertIsNone(refused["exit_code"])
        # Other package files stay out of the compiled copy.
        fixture.call("write_file", {"path": "calc.go", "content": FIXED})
        fixture.call("write_file", {"path": "effect_test.go", "content": effect.replace("Double", "Other")})
        self.assertTrue(fixture.call("run_test", {})["ok"])
        self.assertFalse(self.escape.exists())
