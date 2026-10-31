#!/usr/bin/env python3
"""Exercise host-local rapid integration in one isolated synthetic repository."""

import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import git_environment

git_environment.scrub_inherited()
ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/rapid-integration.py"


class RapidIntegrationTest(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory()
        self.addCleanup(self.scratch.cleanup)
        self.root = Path(self.scratch.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.env = {**os.environ, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull}
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Synthetic Author")
        self.git("config", "user.email", "synthetic@example.invalid")
        for path, body in {
            ".gitignore": "__pycache__/\n",
            ".github/workflows/ci.yml": 'jobs:\n  verify-go:\n    steps:\n      - run: python3 gate.py\n',
            "gate.py": '''import os, sys, time, subprocess
print("verify-go output")
if os.getenv("EXPIRE_GATE"):
    time.sleep(1)
if os.getenv("MUTATE_GATE"):
    open("internal/example/example.go", "a").write("// changed during gate\\n")
if os.getenv("MOVE_TARGET"):
    subprocess.run(["git", "update-ref", "refs/heads/rapid/synthetic", "HEAD"], check=True)
sys.exit(int(os.getenv("FAIL_GATE", "0")))
''',
            "go.mod": "module example.invalid/synthetic\n\ngo 1.26\n",
            "internal/example/example.go": "package example\n\nfunc Value() int { return 1 }\n",
            "internal/example/example_test.go": 'package example\n\nimport "testing"\n\nfunc TestValue(t *testing.T) { if Value() < 1 { t.Fatal("invalid") } }\n',
            "bin/oc-test": '#!/bin/sh\n[ "$1" = broad ] && [ "$2" = -- ] || exit 2\nshift 2\nexec "$@"\n',
        }.items():
            file = self.repo / path
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text(body)
        (self.repo / "bin/oc-test").chmod(0o755)
        (self.repo / "scripts").mkdir()
        for name in ("check-commit-title.py", "release.py"):
            (self.repo / "scripts" / name).write_bytes((ROOT / "scripts" / name).read_bytes())
        self.git("add", ".")
        self.git("commit", "-m", "test: initialize synthetic repository")
        self.base = self.git("rev-parse", "HEAD")
        self.git("branch", "rapid/synthetic")
        # Native worktrees here are fixture state, not host implementation isolation.
        self.candidate_tree = self.root / "candidate"
        self.git("worktree", "add", "-b", "change", str(self.candidate_tree))
        (self.candidate_tree / "internal/example/example.go").write_text("package example\n\nfunc Value() int { return 2 }\n")
        self.git("-C", str(self.candidate_tree), "commit", "-am", "fix: adjust synthetic value")
        self.candidate = self.git("rev-parse", "change")
        self.review = self.root / "review.txt"
        self.review.write_text("Synthetic independent review fixture: accepted exact candidate.\n")
        self.request = {
            "target_ref": "refs/heads/rapid/synthetic",
            "target_tip": self.base,
            "candidate": self.candidate,
            "expires_at": (dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=1)).isoformat(),
            "declaration_ref": "synthetic:operator-declaration",
            "review_ref": "synthetic:independent-review",
            "review_digest": "sha256:" + hashlib.sha256(self.review.read_bytes()).hexdigest(),
            "reviewed_commit": self.candidate,
            "eligibility_ref": "synthetic:eligible-pure-go-change",
            "eligible_paths": ["internal/example/example.go"],
            "title": "fix: integrate synthetic change",
        }

    def git(self, *args):
        result = subprocess.run(["git", "-C", str(self.repo), *args], env=self.env,
                                capture_output=True, text=True, timeout=30, check=True)
        return result.stdout.strip()

    def run_route(self, evidence=None, **env):
        packet = self.root / "request.json"
        packet.write_text(json.dumps(self.request))
        return subprocess.run([sys.executable, str(SCRIPT), "--worktree", str(self.candidate_tree),
                               "--request", str(packet), "--review", str(self.review),
                               "--evidence-dir", str(evidence or self.root / "evidence")],
                              env={**self.env, **env}, capture_output=True, text=True, timeout=60)

    def assert_unmoved(self):
        self.assertEqual(self.git("rev-parse", "main"), self.base)
        self.assertEqual(self.git("rev-parse", "rapid/synthetic"), self.base)
        self.assertEqual(self.git("rev-parse", "change"), self.candidate)

    def fixture_only_candidate(self, path):
        previous = getattr(self, "fixture_path", None)
        if previous:
            (self.candidate_tree / previous).unlink()
        (self.candidate_tree / "internal/example/example.go").write_text("package example\n\nfunc Value() int { return 1 }\n")
        fixture = self.candidate_tree / path
        fixture.parent.mkdir(parents=True, exist_ok=True)
        fixture.write_text('package store\n\nimport "testing"\n\nfunc TestFixture(t *testing.T) {}\n')
        self.fixture_path = path
        self.git("-C", str(self.candidate_tree), "add", "-A")
        self.git("-C", str(self.candidate_tree), "commit", "-m", "test: adjust synthetic fixture")
        self.candidate = self.git("rev-parse", "change")
        self.request.update(candidate=self.candidate, reviewed_commit=self.candidate, eligible_paths=[path])

    def test_safety_owners_reject_go_paths(self):
        for path in (
            "internal/store/other_test.go",
            "internal/store/example.go",
            "internal/store/subdir/example_test.go",
            "internal/agent/example_test.go",
            "internal/receipt/example_test.go",
            "internal/hostlease/example_test.go",
            "internal/sessionboot/example_test.go",
            "internal/payloadschema/example_test.go",
        ):
            with self.subTest(path=path):
                self.fixture_only_candidate(path)
                result = self.run_route()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("unsupported", result.stderr)
                self.assert_unmoved()

    def test_helper_amendment_cannot_admit_itself(self):
        self.fixture_only_candidate("scripts/rapid-integration.py")
        result = self.run_route()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("only explicitly eligible internal Go paths", result.stderr)
        self.assert_unmoved()

    def test_integrates_exact_candidate_tree_and_retains_gate_digests(self):
        result = self.run_route()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        receipt = json.loads(result.stdout)
        merged = self.git("rev-parse", "rapid/synthetic")
        self.assertEqual(receipt["state"], "phase_integrated")
        self.assertEqual(receipt["merge_commit"], merged)
        self.assertEqual(self.git("show", "-s", "--format=%P", merged), f"{self.base} {self.candidate}")
        self.assertEqual(self.git("rev-parse", merged + "^{tree}"), self.git("rev-parse", self.candidate + "^{tree}"))
        self.assertEqual(self.git("rev-parse", "main"), self.base)
        self.assertEqual(self.git("-C", str(self.candidate_tree), "rev-parse", "HEAD"), self.candidate)
        self.assertEqual(self.git("-C", str(self.candidate_tree), "status", "--porcelain"), "")
        message = self.git("show", "-s", "--format=%B", merged)
        for gate in receipt["gates"]:
            data = (self.root / "evidence" / gate["file"]).read_bytes()
            self.assertEqual(gate["digest"], "sha256:" + hashlib.sha256(data).hexdigest())
            self.assertIn(gate["digest"], message)
        self.assertEqual([g["name"] for g in receipt["gates"]], ["title", "verify-go", "targeted-tests"])
        self.assertIn("verify-go output", (self.root / "evidence/verify-go.log").read_text())
        self.assertIn(self.candidate, message)
        self.assertIn("synthetic:independent-review", message)
        self.assertNotIn("completed", receipt)

    def test_gate_failure_retains_output_without_moving_refs(self):
        result = self.run_route(FAIL_GATE="7")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("verify-go", result.stderr)
        self.assertIn("verify-go output", (self.root / "evidence/verify-go.log").read_text())
        self.assert_unmoved()

    def test_expiry_stale_tip_wrong_review_and_default_ref_refuse(self):
        cases = [
            ("expires_at", "2000-01-01T00:00:00+00:00", "expired"),
            ("target_tip", self.candidate, "target"),
            ("reviewed_commit", self.base, "review"),
            ("target_ref", "refs/heads/main", "rapid"),
        ]
        for key, value, error in cases:
            with self.subTest(key=key):
                old = self.request[key]
                self.request[key] = value
                result = self.run_route()
                self.request[key] = old
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(error, result.stderr)
                self.assert_unmoved()

    def test_missing_review_refuses(self):
        self.review.unlink()
        result = self.run_route()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("review", result.stderr)
        self.assert_unmoved()

    def test_unclassified_changes_refuse(self):
        self.request["eligible_paths"] = [".github/workflows/ci.yml"]
        result = self.run_route()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("eligible", result.stderr)
        self.assert_unmoved()

    def test_divergent_candidate_refuses(self):
        (self.repo / "unrelated").write_text("target-only content\n")
        self.git("add", ".")
        self.git("commit", "-m", "test: advance target")
        new_base = self.git("rev-parse", "HEAD")
        self.git("update-ref", "refs/heads/rapid/synthetic", new_base)
        self.request["target_tip"] = new_base
        result = self.run_route()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("non-divergent", result.stderr)
        self.assertEqual(self.git("rev-parse", "rapid/synthetic"), new_base)

    def test_expiry_during_gate_refuses(self):
        self.request["expires_at"] = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=0.7)).isoformat()
        result = self.run_route(EXPIRE_GATE="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("expired", result.stderr)
        self.assert_unmoved()

    def test_candidate_mutation_during_gate_refuses(self):
        result = self.run_route(MUTATE_GATE="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("remain clean", result.stderr)
        self.assert_unmoved()

    def test_target_movement_during_gate_is_not_overwritten(self):
        result = self.run_route(MOVE_TARGET="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("target tip changed", result.stderr)
        self.assertEqual(self.git("rev-parse", "rapid/synthetic"), self.candidate)
        self.assertEqual(self.git("rev-parse", "main"), self.base)

    def test_checked_out_target_and_executable_commit_hook_refuse(self):
        hook = self.repo / ".git/hooks/commit-msg"
        hook.write_text("#!/bin/sh\nexit 0\n")
        hook.chmod(0o755)
        result = self.run_route()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("commit hooks", result.stderr)
        self.assert_unmoved()
        hook.unlink()
        self.git("worktree", "add", str(self.root / "target"), "rapid/synthetic")
        result = self.run_route()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checked out", result.stderr)
        self.assert_unmoved()

    def test_evidence_inside_any_worktree_refuses(self):
        for root in (self.repo, self.candidate_tree):
            with self.subTest(root=root):
                evidence = root / "evidence"
                result = self.run_route(evidence=evidence)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("outside the repository", result.stderr)
                self.assertFalse(evidence.exists())
                self.assert_unmoved()

    def test_deleted_go_path_refuses(self):
        path = "internal/example/example_test.go"
        (self.candidate_tree / path).unlink()
        self.git("-C", str(self.candidate_tree), "commit", "-am", "test: delete fixture test")
        self.candidate = self.git("rev-parse", "change")
        self.request.update(candidate=self.candidate, reviewed_commit=self.candidate,
                            eligible_paths=["internal/example/example.go", path])
        result = self.run_route()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("deleted Go paths", result.stderr)
        self.assert_unmoved()


if __name__ == "__main__":
    unittest.main()
