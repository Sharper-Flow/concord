#!/usr/bin/env python3
"""Tests for the invocation proof in scripts/evidence_anchors.py.

A `validator` anchor claims a checker is executed. The claim was resolved by
asking whether the script path appeared as text in a workflow or in
`check-json.py`, so a name in a comment, a step title, or an unused variable
counted as proof that a law record is enforced. These tests pin the two
structural resolutions that replaced it: a workflow proves invocation through
its `run:` commands, and nesting proves it through the `subprocess.run` call
graph.

The Go source signature is pinned here against the rglob baseline it replaced:
the scandir walk must observe the same population — including `.go` entries of
any kind, hidden names, excluded `.git`/`vendor` components, and symlink
behavior — on the repository and on deterministic Linux fixtures.
"""
from __future__ import annotations

import errno
import importlib.util
import os
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "evidence_anchors", Path(__file__).with_name("evidence_anchors.py")
)
assert SPEC and SPEC.loader
anchors = importlib.util.module_from_spec(SPEC)
sys.modules["evidence_anchors"] = anchors
SPEC.loader.exec_module(anchors)


def _baseline_signature(root: Path) -> tuple[tuple[str, int, int], ...]:
    """The superseded rglob traversal, pinned verbatim as the baseline."""
    files: list[tuple[str, int, int]] = []
    for path in root.rglob("*.go"):
        if ".git" in path.parts or "vendor" in path.parts:
            continue
        try:
            stat = path.stat()
        except OSError:
            continue
        files.append((path.relative_to(root).as_posix(), stat.st_mtime_ns, stat.st_size))
    return tuple(sorted(files))


def _candidate_signature(root: Path) -> tuple[tuple[str, int, int], ...]:
    original = anchors.ROOT
    anchors.ROOT = root
    try:
        return anchors._go_test_source_signature()
    finally:
        anchors.ROOT = original


class _VanishedEntry:
    """A directory entry that disappears between listing and stat."""

    def __init__(self, path: str) -> None:
        self.name = "vanished.go"
        self.path = path
        self.touched = False

    def is_dir(self, follow_symlinks: bool = True) -> bool:
        self.touched = True
        return False

    def stat(self, follow_symlinks: bool = True) -> os.stat_result:
        self.touched = True
        raise FileNotFoundError(errno.ENOENT, "vanished before stat", self.path)


class _ScanResult:
    def __init__(self, entries: list[object]) -> None:
        self._entries = entries

    def __enter__(self):
        return iter(self._entries)

    def __exit__(self, *exception: object) -> bool:
        return False

    def __iter__(self):
        return iter(self._entries)


class _ScanPlan:
    """Deterministic directory-scan interception for both traversals.

    The plan is installed on every seam the running interpreter's rglob and
    the scandir candidate share: `os.scandir` (the candidate, and pathlib on
    Python 3.12) and the pathlib globber's captured scandir (Python 3.13).
    A plan no implementation consulted fails the control rather than pass it
    as silently skipped coverage.
    """

    def __init__(self, fail_dirs: set[str] | None = None, extras: dict[str, list[object]] | None = None) -> None:
        self.fail_dirs = fail_dirs or set()
        self.extras = extras or {}
        self.scanned: list[str] = []

    def install(self) -> list[tuple[object, str, object]]:
        import pathlib

        real = os.scandir

        def wrapper(target: object) -> _ScanResult:
            normalized = os.path.normpath(os.fspath(target))
            self.scanned.append(normalized)
            if normalized in self.fail_dirs:
                raise OSError(errno.EIO, "simulated scan failure")
            entries: list[object] = list(real(normalized))
            entries.extend(self.extras.get(normalized, ()))
            return _ScanResult(entries)

        patches: list[tuple[object, str, object]] = []
        os.scandir = wrapper
        patches.append((os, "scandir", real))
        globber = getattr(pathlib.Path, "_globber", None)
        if globber is not None and hasattr(globber, "scandir"):
            original = globber.scandir
            globber.scandir = staticmethod(wrapper)
            patches.append((globber, "scandir", original))
        return patches

    @staticmethod
    def restore(patches: list[tuple[object, str, object]]) -> None:
        for owner, name, value in reversed(patches):
            if name == "scandir" and owner is not os:
                owner.scandir = staticmethod(value)
            else:
                setattr(owner, name, value)


def test_workflow_commands_keep_run_bodies() -> None:
    text = "\n".join(
        [
            "      - name: Validate",
            "        run: python3 scripts/check-one.py",
            "      - name: Bundle",
            "        run: |",
            "          python3 scripts/check-two.py",
            "          python3 scripts/check-three.py",
        ]
    )
    commands = anchors.workflow_commands(text)
    for script in ("scripts/check-one.py", "scripts/check-two.py", "scripts/check-three.py"):
        assert script in commands, f"{script} missing from extracted commands"


def test_workflow_commands_drop_prose() -> None:
    text = "\n".join(
        [
            "      # scripts/check-comment.py explains the rule",
            "      - name: Validate with scripts/check-title.py",
            "        run: python3 scripts/check-real.py",
        ]
    )
    commands = anchors.workflow_commands(text)
    assert "scripts/check-real.py" in commands
    assert "scripts/check-comment.py" not in commands, "a comment counted as a command"
    assert "scripts/check-title.py" not in commands, "a step name counted as a command"


def test_nested_invocations_find_subprocess_calls(tmp: Path) -> None:
    module = tmp / "nesting.py"
    module.write_text(
        "\n".join(
            [
                "import subprocess",
                "import sys",
                "ROOT = __file__",
                "checker = 'scripts/check-bound.py'",
                "subprocess.run([sys.executable, checker])",
                "subprocess.run([sys.executable, 'scripts/check-literal.py'])",
            ]
        ),
        encoding="utf-8",
    )
    found = anchors.nested_invocations(module)
    assert "scripts/check-bound.py" in found, "variable-bound invocation missed"
    assert "scripts/check-literal.py" in found, "literal invocation missed"


def test_nested_invocations_reject_mentions(tmp: Path) -> None:
    module = tmp / "mentions.py"
    module.write_text(
        "\n".join(
            [
                "import subprocess",
                "import sys",
                "# scripts/check-comment.py is related",
                "'''scripts/check-docstring.py is also related'''",
                "unused = 'scripts/check-unused.py'",
                "subprocess.run([sys.executable, 'scripts/check-real.py'])",
            ]
        ),
        encoding="utf-8",
    )
    found = anchors.nested_invocations(module)
    assert found == {"scripts/check-real.py"}, f"mentions counted as invocations: {sorted(found)}"


def test_unparseable_module_proves_nothing(tmp: Path) -> None:
    module = tmp / "broken.py"
    module.write_text("def (:\n", encoding="utf-8")
    assert anchors.nested_invocations(module) == set()



def test_adapter_test_anchor_resolves() -> None:
    findings: list[str] = []
    anchors.check_anchor(
        {"kind": "adapter_test", "value": "adapter/opencode/concord.test.ts#a read answered by a newer core contract is typed as version skew"},
        "prefix",
        findings,
    )
    assert findings == [], findings


def test_adapter_test_anchor_rejects_unknown_test() -> None:
    findings: list[str] = []
    anchors.check_anchor(
        {"kind": "adapter_test", "value": "adapter/opencode/concord.test.ts#no such test is registered"},
        "prefix",
        findings,
    )
    assert any("does not resolve" in f for f in findings), findings


def test_adapter_test_anchor_rejects_foreign_path() -> None:
    findings: list[str] = []
    anchors.check_anchor(
        {"kind": "adapter_test", "value": "adapter/elsewhere/concord.test.ts#a read answered by a newer core contract is typed as version skew"},
        "prefix",
        findings,
    )
    assert any("must read" in f for f in findings), findings


def test_contract_checker_anchor_resolves_through_the_umbrella_nesting() -> None:
    # The complete contract check has no direct CI step: check-json.py owns
    # it, so the validator anchor must resolve through the subprocess call
    # graph alone. If the nesting is ever dropped, every coverage record
    # citing scripts/check-agent-contracts.py stops resolving here.
    nested = anchors.nested_invocations(ROOT / "scripts/check-json.py")
    assert "scripts/check-agent-contracts.py" in nested, sorted(nested)
    assert anchors.validator_runs_in_ci("scripts/check-agent-contracts.py")


def test_the_adapter_suite_remains_a_direct_workflow_invocation() -> None:
    # adapter_test anchors hold only while a required workflow runs the
    # suite directly; the nested contract check running the same suite does
    # not satisfy the anchor machinery, so losing the direct step must fail.
    assert anchors.adapter_suite_runs_in_ci()


def _build_traversal_fixture(root: Path) -> Path:
    """A deterministic Linux tree covering the traversal's edge population."""
    root.mkdir(parents=True)
    (root / "plain.go").write_text("package f\n", encoding="utf-8")
    (root / ".hidden.go").write_text("package f\n", encoding="utf-8")
    (root / ".go").write_text("package f\n", encoding="utf-8")
    (root / "vendor.go").write_text("package f\n", encoding="utf-8")
    (root / "ünïcode.go").write_text("package f\n", encoding="utf-8")
    unicode_directory = root / "日本語"
    unicode_directory.mkdir()
    (unicode_directory / "テスト.go").write_text("package f\n", encoding="utf-8")

    nested = root / "deep" / "nest"
    nested.mkdir(parents=True)
    (nested / "leaf.go").write_text("package f\n", encoding="utf-8")
    excluded_git = root / "deep" / ".git"
    excluded_git.mkdir()
    (excluded_git / "hidden.go").write_text("package f\n", encoding="utf-8")
    excluded_vendor = root / "deep" / "vendor"
    excluded_vendor.mkdir()
    (excluded_vendor / "suppressed.go").write_text("package f\n", encoding="utf-8")
    # Only exact `.git`/`vendor` components exclude; look-alike names stay.
    lookalike = root / "x.git" / "vendors"
    lookalike.mkdir(parents=True)
    (lookalike / "kept.go").write_text("package f\n", encoding="utf-8")

    # A directory named `*.go` is itself an entry and is still descended into.
    go_directory = root / "cmd.go"
    go_directory.mkdir()
    (go_directory / "inner.go").write_text("package f\n", encoding="utf-8")

    real_directory = root / "realdir"
    real_directory.mkdir()
    (real_directory / "inside.go").write_text("package f\n", encoding="utf-8")
    # Leaf symlinks stat their target; symlinked directories are not entered.
    os.symlink(nested / "leaf.go", root / "live.go")
    os.symlink(real_directory, root / "dirlink")
    os.symlink(real_directory, root / "dirlink.go")
    os.symlink(root / "gone" / "nowhere.go", root / "dead.go")
    return root


def test_source_signature_matches_rglob_on_the_repository() -> None:
    baseline = _baseline_signature(ROOT)
    candidate = anchors._go_test_source_signature()
    assert candidate == baseline, "scandir walk diverged from rglob on the repository"
    assert baseline, "the repository signature must not be empty"
    assert list(candidate) == sorted(candidate), "signature must stay sorted"


def test_source_signature_matches_rglob_on_traversal_fixtures(tmp: Path) -> None:
    root = _build_traversal_fixture(tmp / "fixture")
    baseline = _baseline_signature(root)
    candidate = _candidate_signature(root)
    assert candidate == baseline, f"scandir walk diverged: {set(candidate) ^ set(baseline)}"
    present = {path for path, _, _ in candidate}
    for expected in (
        "plain.go",
        ".hidden.go",
        ".go",
        "vendor.go",
        "ünïcode.go",
        "日本語/テスト.go",
        "deep/nest/leaf.go",
        "x.git/vendors/kept.go",
        "cmd.go",
        "cmd.go/inner.go",
        "realdir/inside.go",
        "live.go",
        "dirlink.go",
    ):
        assert expected in present, f"expected {expected} in the signature"
    for absent in (
        "deep/.git/hidden.go",
        "deep/vendor/suppressed.go",
        "dead.go",
        "dirlink/inside.go",
    ):
        assert absent not in present, f"{absent} must not resolve"

    # A leaf symlink reports the target's metadata, not the link's.
    target = os.stat(root / "deep" / "nest" / "leaf.go")
    by_path = {path: (mtime, size) for path, mtime, size in candidate}
    assert by_path["live.go"] == (target.st_mtime_ns, target.st_size)
    directory_target = os.stat(root / "realdir")
    assert by_path["dirlink.go"] == (directory_target.st_mtime_ns, directory_target.st_size)


def test_source_signature_excludes_roots_under_git_or_vendor(tmp: Path) -> None:
    for component in (".git", "vendor"):
        excluded_root = tmp / component / "inner"
        excluded_root.mkdir(parents=True)
        (excluded_root / "x.go").write_text("package f\n", encoding="utf-8")
        baseline = _baseline_signature(excluded_root)
        candidate = _candidate_signature(excluded_root)
        assert candidate == baseline == (), f"root under {component} must exclude everything"

    # The exclusion is exact-component, not substring: look-alike ancestors stay.
    boundary_root = tmp / "x.git" / "vendors" / "inner"
    boundary_root.mkdir(parents=True)
    (boundary_root / "x.go").write_text("package f\n", encoding="utf-8")
    assert _candidate_signature(boundary_root) == _baseline_signature(boundary_root)
    assert len(_candidate_signature(boundary_root)) == 1


def test_source_signature_skips_a_directory_that_cannot_be_scanned(tmp: Path) -> None:
    root = tmp / "scan-failure"
    root.mkdir()
    (root / "kept.go").write_text("package f\n", encoding="utf-8")
    unreadable = root / "poisoned"
    unreadable.mkdir()
    (unreadable / "lost.go").write_text("package f\n", encoding="utf-8")

    plan = _ScanPlan(fail_dirs={str(unreadable)})
    patches = plan.install()
    try:
        baseline = _baseline_signature(root)
        assert str(root) in plan.scanned, "baseline scan seam was not intercepted"
        plan.scanned.clear()
        candidate = _candidate_signature(root)
        assert str(root) in plan.scanned, "candidate scan seam was not intercepted"
    finally:
        plan.restore(patches)
    assert candidate == baseline, "a failed scan must drop the subtree in both traversals"
    present = {path for path, _, _ in candidate}
    assert "kept.go" in present and "poisoned/lost.go" not in present, sorted(present)


def test_source_signature_skips_entries_removed_before_stat(tmp: Path) -> None:
    root = tmp / "vanished"
    root.mkdir()
    (root / "present.go").write_text("package f\n", encoding="utf-8")
    vanished = _VanishedEntry(str(root / "vanished.go"))

    plan = _ScanPlan(extras={str(root): [vanished]})
    patches = plan.install()
    try:
        baseline = _baseline_signature(root)
        assert str(root) in plan.scanned, "baseline scan seam was not intercepted"
        assert vanished.touched, "the vanished entry reached neither traversal"
        plan.scanned.clear()
        vanished.touched = False
        candidate = _candidate_signature(root)
        assert str(root) in plan.scanned, "candidate scan seam was not intercepted"
        assert vanished.touched, "the vanished entry reached neither traversal"
    finally:
        plan.restore(patches)
    assert candidate == baseline, "an entry removed before stat must drop in both traversals"
    present = {path for path, _, _ in candidate}
    assert "present.go" in present and "vanished.go" not in present, sorted(present)


def test_source_signature_of_a_missing_root_is_empty(tmp: Path) -> None:
    absent_root = tmp / "does-not-exist"
    assert _candidate_signature(absent_root) == _baseline_signature(absent_root) == ()


def main() -> int:
    import tempfile

    tests = [
        value
        for name, value in sorted(globals().items())
        if name.startswith("test_") and callable(value)
    ]
    failures = 0
    for test in tests:
        try:
            if test.__code__.co_argcount:
                with tempfile.TemporaryDirectory() as directory:
                    test(Path(directory))
            else:
                test()
            print(f"ok  {test.__name__}")
        except AssertionError as err:
            failures += 1
            print(f"FAIL {test.__name__}: {err}")
    print(f"evidence anchor tests passed: {len(tests) - failures}/{len(tests)}")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
