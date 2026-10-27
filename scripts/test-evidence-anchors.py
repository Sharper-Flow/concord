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


class _FakeEntry:
    """A directory entry whose metadata observation fails deterministically.

    ``stat_errno`` selects the failure the entry produces: ``ENOENT`` models
    a leaf removed between listing and stat, any other errno a leaf whose
    metadata cannot be read at all. ``stat_calls`` and ``is_dir_calls`` stay
    separate because pathlib's own recursion calls ``is_dir`` on injected
    entries; a shared counter would let a directory probe pose as the stat
    error the control exists to prove.
    """

    def __init__(self, path: str, name: str, stat_errno: int) -> None:
        self.name = name
        self.path = path
        self.stat_errno = stat_errno
        self.stat_calls = 0
        self.is_dir_calls = 0

    def is_dir(self, follow_symlinks: bool = True) -> bool:
        self.is_dir_calls += 1
        return False

    def stat(self, follow_symlinks: bool = True) -> os.stat_result:
        self.stat_calls += 1
        raise OSError(self.stat_errno, os.strerror(self.stat_errno), self.path)


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


class _PathStatPlan:
    """Deterministic ``pathlib.Path.stat`` interception for the rglob baseline.

    The candidate stats through ``DirEntry.stat`` and never calls
    ``Path.stat``, so this seam reaches the baseline alone — which is what
    makes the two stat-error controls independent. Every call is recorded;
    chosen paths fail with EIO, a non-ENOENT error that no permission state
    can produce accidentally.
    """

    def __init__(self, fail_paths: set[str] | None = None) -> None:
        self.fail_paths = fail_paths or set()
        self.seen: list[str] = []
        self.errors: list[str] = []

    def install(self) -> list[tuple[object, str, object]]:
        import pathlib

        plan = self
        original = pathlib.Path.stat

        def wrapper(path: Path, *args: object, **kwargs: object) -> os.stat_result:
            normalized = os.fspath(path)
            plan.seen.append(normalized)
            if normalized in plan.fail_paths:
                plan.errors.append(normalized)
                raise OSError(errno.EIO, "simulated leaf-stat failure", normalized)
            return original(path, *args, **kwargs)

        pathlib.Path.stat = wrapper
        return [(pathlib.Path, "stat", original)]

    @staticmethod
    def restore(patches: list[tuple[object, str, object]]) -> None:
        for owner, name, value in reversed(patches):
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


def test_scenario_identity_is_not_an_arbitrary_nested_json_id(tmp: Path) -> None:
    from unittest import mock
    corpus = tmp / ".concord/scenarios"
    corpus.mkdir(parents=True)
    (corpus / "synthetic.json").write_text('{"fixtures":{"id":"not-a-scenario"},"scenarios":[]}')
    with mock.patch.object(anchors, "ROOT", tmp):
        assert not anchors.scenario_exists("not-a-scenario"), "fixture id became a scenario"


def test_ambiguous_scenario_declarations_do_not_resolve(tmp: Path) -> None:
    from unittest import mock
    corpus = tmp / ".concord/scenarios"
    corpus.mkdir(parents=True)
    (corpus / "one.json").write_text('{"scenarios":[{"id":"scenario-duplicate"}]}')
    (corpus / "two.json").write_text('{"scenarios":[{"id":"scenario-duplicate"}]}')
    with mock.patch.object(anchors, "ROOT", tmp):
        assert not anchors.scenario_exists("scenario-duplicate")


def test_corpus_presence_without_source_owner_proves_nothing(tmp: Path) -> None:
    from unittest import mock
    corpus = tmp / ".concord/scenarios"
    corpus.mkdir(parents=True)
    (corpus / "synthetic.json").write_text('{"scenarios":[{"id":"unbound-scenario"}]}')
    with mock.patch.object(anchors, "ROOT", tmp):
        findings = []
        anchors.check_anchor({"kind": "scenario", "value": "unbound-scenario"}, "fixture", findings)
        assert findings, "corpus registration became execution coverage"


def test_continuity_source_binds_seven_scenarios_and_leaves_the_excluded_owner_unproved() -> None:
    declarations = anchors.scenario_declarations()
    scenarios = {identity for identity, (path, _) in declarations.items() if path == ".concord/scenarios/adapter-continuity.v1.json"}
    assert len(scenarios) == 8, scenarios
    unbound = {identity for identity in scenarios if anchors.scenario_adapter_test(identity) is None}
    assert unbound == {"adapter-continuity-session-vacate-then-reclaim-passes-occupancy-gate"}, unbound


def test_continuity_source_must_check_every_assertion_and_reject_mentions(tmp: Path) -> None:
    import json
    from unittest import mock
    corpus_dir = tmp / ".concord/scenarios"
    corpus_dir.mkdir(parents=True)
    corpus_path = corpus_dir / "adapter-continuity.v1.json"
    document = json.loads((ROOT / ".concord/scenarios/adapter-continuity.v1.json").read_text())
    corpus_path.write_text(json.dumps(document))
    adapter = tmp / "adapter/opencode"
    adapter.mkdir(parents=True)
    source = (ROOT / "adapter/opencode/continuity_hook.test.ts").read_text()
    target = adapter / "continuity_hook.test.ts"
    identity = "adapter-continuity-byte-stable-across-turns"
    with mock.patch.object(anchors, "ROOT", tmp):
        target.write_text(source)
        assert anchors.scenario_adapter_test(identity) == "adapter/opencode/continuity_hook.test.ts"
        for changed in (
            source.replace('test("' + identity + '"', 'test.skip("' + identity + '"'),
            source.replace('test("' + identity + '"', 'test("not-the-scenario"'),
            source.replace("system_prompt_byte_identical: () => expect(first.system[0]).toBe(second.system[0])",
                           "system_prompt_byte_identical: () => true"),
            source.replace("no_duplicate_sentinel: () =>", "missing_assertion: () =>"),
            "/*" + source + "*/",
            source + '\ntest("' + identity + '", async () => {})',
        ):
            target.write_text(changed)
            assert anchors.scenario_adapter_test(identity) is None, "source mention or incomplete assertions became proof"
        target.write_text(source)
        document["scenarios"][0]["then"].append({"assertion": "new_unchecked_assertion"})
        corpus_path.write_text(json.dumps(document))
        assert anchors.scenario_adapter_test(identity) is None, "new corpus assertion acquired invented coverage"


def test_go_scenario_source_requires_exact_nested_witness_and_pinned_corpus(tmp: Path) -> None:
    import json
    from unittest import mock
    corpus = tmp / ".concord/scenarios"
    corpus.mkdir(parents=True)
    (corpus / "fixture.json").write_text('{"scenarios":[{"id":"scenario-exact"}]}')
    package = tmp / "internal/fixture"
    package.mkdir(parents=True)
    (package / "corpus_test.go").write_text('package fixture\nfunc TestCorpus(t *testing.T) { t.Fatal("fixture") }')
    recipes = tmp / ".concord/oracles"
    recipes.mkdir()
    recipe = {"kind": "go_top_level_v1", "package_cwd": "internal/fixture",
              "test_files": ["internal/fixture/corpus_test.go"],
              "fixture_files": [".concord/scenarios/fixture.json"],
              "cases": {"case:corpus": ["TestCorpus/scenario-exact"]}}
    path = recipes / "fixture.json"
    with mock.patch.object(anchors, "ROOT", tmp), mock.patch.object(anchors, "go_test_asserts", return_value=True):
        path.write_text(json.dumps(recipe))
        assert anchors.scenario_source_bound("scenario-exact")
        for witness in ("TestCorpus", "TestCorpus/scenario-other"):
            recipe["cases"]["case:corpus"] = [witness]
            path.write_text(json.dumps(recipe))
            assert not anchors.scenario_source_bound("scenario-exact")
        recipe["cases"]["case:corpus"] = ["TestCorpus/scenario-exact"]
        recipe["fixture_files"] = []
        path.write_text(json.dumps(recipe))
        assert not anchors.scenario_source_bound("scenario-exact")


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
    vanished = _FakeEntry(str(root / "vanished.go"), "vanished.go", errno.ENOENT)

    plan = _ScanPlan(extras={str(root): [vanished]})
    stat_plan = _PathStatPlan()
    patches = plan.install()
    stat_patches = stat_plan.install()
    try:
        baseline = _baseline_signature(root)
        assert str(root) in plan.scanned, "baseline scan seam was not intercepted"
        assert str(root / "vanished.go") in stat_plan.seen, "baseline never stat-ed the vanished leaf"
        plan.scanned.clear()
        stat_plan.seen.clear()
        vanished.stat_calls = 0
        vanished.is_dir_calls = 0
        candidate = _candidate_signature(root)
        assert str(root) in plan.scanned, "candidate scan seam was not intercepted"
        assert vanished.stat_calls >= 1, "candidate's stat error path did not run"
    finally:
        stat_plan.restore(stat_patches)
        plan.restore(patches)
    assert candidate == baseline, "an entry removed before stat must drop in both traversals"
    present = {path for path, _, _ in candidate}
    assert "present.go" in present and "vanished.go" not in present, sorted(present)


def test_source_signature_skips_a_leaf_whose_stat_fails(tmp: Path) -> None:
    """A non-ENOENT stat failure drops one leaf and keeps its sibling.

    The baseline seam (``Path.stat``) and the candidate seam (an injected
    ``DirEntry.stat``) fail independently and deterministically with EIO, so
    the control cannot pass by one traversal silently skipping the entry.
    """
    root = tmp / "stat-failure"
    root.mkdir()
    (root / "kept.go").write_text("package f\n", encoding="utf-8")
    poison_path = str(root / "poison.go")
    poison = _FakeEntry(poison_path, "poison.go", errno.EIO)

    plan = _ScanPlan(extras={str(root): [poison]})
    stat_plan = _PathStatPlan(fail_paths={poison_path})
    patches = plan.install()
    stat_patches = stat_plan.install()
    try:
        baseline = _baseline_signature(root)
        assert str(root) in plan.scanned, "baseline scan seam was not intercepted"
        assert poison_path in stat_plan.errors, "baseline's stat error path did not run"
        plan.scanned.clear()
        stat_plan.seen.clear()
        stat_plan.errors.clear()
        poison.stat_calls = 0
        poison.is_dir_calls = 0
        candidate = _candidate_signature(root)
        assert str(root) in plan.scanned, "candidate scan seam was not intercepted"
        assert poison.stat_calls >= 1, "candidate's stat error path did not run"
    finally:
        stat_plan.restore(stat_patches)
        plan.restore(patches)
    assert candidate == baseline, "a failed leaf stat must drop the entry in both traversals"
    present = {path for path, _, _ in candidate}
    assert "kept.go" in present and "poison.go" not in present, sorted(present)


def test_source_signature_of_a_missing_root_is_empty(tmp: Path) -> None:
    absent_root = tmp / "does-not-exist"
    assert _candidate_signature(absent_root) == _baseline_signature(absent_root) == ()


def test_go_test_projector_failure_is_reported_as_tool_failure() -> None:
    from unittest import mock

    findings: list[str] = []
    with mock.patch.object(anchors, "go_test_asserts", side_effect=anchors.GoTestProjectionError("synthetic projector failure")):
        try:
            anchors.check_anchor({"kind": "go_test", "value": "internal/fixture.TestProof"}, "fixture", findings)
        except anchors.GoTestProjectionError as exc:
            raise AssertionError(f"projector failure escaped the anchor boundary: {exc}") from exc
    assert findings == ["fixture: go_test projector failed: synthetic projector failure"], findings


def test_go_test_projector_failure_does_not_repeat_for_each_anchor() -> None:
    import subprocess
    from unittest import mock

    findings: list[str] = []
    result = subprocess.CompletedProcess([], 0, "{broken", "")
    with mock.patch.object(anchors, "_GO_TEST_SIGNATURE", None), mock.patch.object(anchors, "_GO_TEST_FACTS", {}), mock.patch.object(anchors.subprocess, "run", return_value=result) as run:
        for name in ("TestFirst", "TestSecond"):
            anchors.check_anchor({"kind": "go_test", "value": f"internal/fixture.{name}"}, name, findings)
    assert run.call_count == 1, run.call_count
    assert len(findings) == 2 and all("go_test projector failed:" in finding for finding in findings), findings


def test_go_test_projector_failure_is_invalidated_by_source_change(tmp: Path) -> None:
    import subprocess
    from unittest import mock

    failed = subprocess.CompletedProcess([], 1, "", "synthetic projector failure")
    valid = subprocess.CompletedProcess([], 0, '{"schema_version":"1.0","packages":{"fixture":{"TestRestored":true}}}', "")
    with mock.patch.object(anchors, "_GO_TEST_SIGNATURE", None), mock.patch.object(anchors, "_GO_TEST_FACTS", {}), mock.patch.object(anchors.subprocess, "run", side_effect=[failed, valid]) as run:
        try:
            anchors.go_test_exists("fixture", "TestRestored", tmp)
        except anchors.GoTestProjectionError:
            pass
        else:
            raise AssertionError("failed projector supplied declaration presence")
        (tmp / "fixture").mkdir()
        (tmp / "fixture/restored_test.go").write_text('package fixture\nimport "testing"\nfunc TestRestored(t *testing.T) { t.Fatal("proof") }\n')
        assert anchors.go_test_exists("fixture", "TestRestored", tmp)
    assert run.call_count == 2, run.call_count


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
