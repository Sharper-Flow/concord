#!/usr/bin/env python3
"""MCP observation ingestion and the shared offline pointer snapshot."""
import importlib.util
import io
import json
import sys
import tempfile
from contextlib import contextmanager, redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import coverage_state

spec = importlib.util.spec_from_file_location("update_issue_state", ROOT / "scripts/update-issue-state.py")
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


def observation(identifier="CON-1", status_type="started", read_at="2020-01-01T00:00:00Z"):
    return {"identifier": identifier, "status_type": status_type, "read_at": read_at}


@contextmanager
def fixture(pointers=None, document=None):
    with tempfile.TemporaryDirectory(prefix="issue-observations-") as directory:
        root = Path(directory)
        snapshot = root / "snapshot.json"
        snapshot.write_text("unchanged\n", encoding="utf-8")
        input_path = root / "input.json"
        input_path.write_text(json.dumps(document or {"schema_version": "1.0", "observations": [observation()]}), encoding="utf-8")
        with mock.patch.object(updater, "collect_outstanding_issues", return_value=["CON-1"] if pointers is None else pointers), mock.patch.multiple(
            updater, ROOT=root, ISSUE_STATE=snapshot
        ), mock.patch.multiple(coverage_state, ROOT=root, ISSUE_STATE=snapshot), mock.patch.object(
            updater.subprocess, "run", side_effect=AssertionError("no implicit network/Go route")
        ):
            yield input_path, snapshot


def run(*arguments):
    output = io.StringIO()
    with mock.patch.object(sys, "argv", ["update-issue-state.py", *map(str, arguments)]), redirect_stdout(output), redirect_stderr(output):
        code = updater.main()
    return code, output.getvalue()


def test_missing_input_names_required_identifiers_without_writing():
    with fixture(["CON-1", "CON-2"]) as (_, snapshot):
        code, output = run()
        assert code == 1 and "CON-1" in output and "CON-2" in output, output
        assert snapshot.read_text() == "unchanged\n"


def test_raw_status_mapping_and_recorded_read_time():
    for status in ("completed", "canceled", "duplicate", "triage", "backlog", "unstarted", "started"):
        document = {"schema_version": "1.0", "observations": [observation(status_type=status)]}
        with fixture(document=document) as (input_path, snapshot):
            code, output = run("--linear-observations", input_path)
            assert code == 0, output
            stored = json.loads(snapshot.read_text())
            assert stored["issues"] == {"CON-1": "closed" if status in ("completed", "canceled", "duplicate") else "open"}
            assert stored["linear_read_at"] == {"CON-1": "2020-01-01T00:00:00Z"}
            findings = []
            assert coverage_state.load_issue_states(findings) == stored["issues"], findings
            coverage_state.check_outstanding_pointer({"issue": "CON-1"}, "fixture", stored["issues"], findings)
            assert bool(findings) == (stored["issues"]["CON-1"] == "closed"), findings


def test_bad_observations_never_write():
    bad_rows = [
        [], [observation(), observation()], [observation("CON-2")],
        [observation(status_type="unknown")], [observation(status_type=[])],
        [observation(read_at="not-a-time")], [observation(read_at="9999-01-01T00:00:00Z")],
        [observation(read_at="2020-01-01T00:00:00+01:00")],
        [{**observation(), "source": "linear_mcp"}],
        [{key: value for key, value in observation().items() if key != "status_type"}],
        [{**observation(), "identifier": []}], [None],
    ]
    for rows in bad_rows:
        with fixture(document={"schema_version": "1.0", "observations": rows}) as (input_path, snapshot):
            code, output = run("--linear-observations", input_path)
            assert code == 1, (rows, output)
            assert snapshot.read_text() == "unchanged\n", rows


def test_missing_partial_input_lists_identifiers():
    with fixture(["CON-1", "CON-2"]) as (input_path, snapshot):
        code, output = run("--linear-observations", input_path)
        assert code == 1 and "CON-2" in output, output
        assert snapshot.read_text() == "unchanged\n"


def test_bad_files_and_closed_document_shape_never_write():
    rows = json.dumps([observation()])
    for text in ("{", "[]", f'{{"schema_version":"1.0","schema_version":"1.0","observations":{rows}}}',
                 f'{{"schema_version":"2.0","observations":{rows}}}',
                 f'{{"schema_version":"1.0","observations":{rows},"extra":true}}',
                 '{"schema_version":"1.0","observations":{}}'):
        with fixture() as (input_path, snapshot):
            input_path.write_text(text)
            code, output = run("--linear-observations", input_path)
            assert code == 1 and snapshot.read_text() == "unchanged\n", output
    with fixture() as (input_path, snapshot):
        input_path.unlink()
        code, output = run("--linear-observations", input_path)
        assert code == 1 and snapshot.read_text() == "unchanged\n", output


def test_no_linear_pointers_and_mixed_provider_refresh():
    with fixture([]) as (_, snapshot):
        code, output = run()
        assert code == 0 and json.loads(snapshot.read_text())["linear_read_at"] == {}, output
    with fixture([]) as (input_path, snapshot):
        code, output = run("--linear-observations", input_path)
        assert code == 1 and snapshot.read_text() == "unchanged\n", output
    with fixture([123, "CON-1"]) as (input_path, snapshot), mock.patch.object(updater, "resolve_github", return_value="open") as github:
        code, output = run("--linear-observations", input_path)
        assert code == 0, output
        github.assert_called_once_with(123)
        assert json.loads(snapshot.read_text())["issues"] == {"123": "open", "CON-1": "open"}


def test_collection_merges_planes_without_duplicates():
    with tempfile.TemporaryDirectory(prefix="issue-planes-") as directory:
        root = Path(directory)
        items = root / "items-plane.json"
        items.write_text(json.dumps({"items": [
            {"id": "a2-legacy", "state": "outstanding", "issue": 41},
            {"id": "a1-done", "state": "satisfied", "evidence": [{"kind": "validator", "value": "v"}]},
            {"id": "b2-blocked", "state": "outstanding", "issue": "CON-72"},
            {"id": "p2-shape", "state": "outstanding", "issue": "not-an-issue"},
        ]}), encoding="utf-8")
        records = root / "records-plane.json"
        records.write_text(json.dumps({"records": [
            {"state": "outstanding", "issue": "CON-72"},
            {"state": "outstanding", "issue": "CON-77"},
        ]}), encoding="utf-8")
        with mock.patch.object(coverage_state, "ISSUE_STATE_MANIFESTS", ((items, "items"), (records, "records"))):
            collected = coverage_state.collect_outstanding_issues()
        assert collected == [41, "CON-72", "CON-77"], collected


def test_snapshot_metadata_rejects_missing_extra_invalid_or_later_times():
    for times in (None, {}, {"CON-2": "2020-01-01T00:00:00Z"}, {"CON-1": "bad"}, {"CON-1": "2021-01-01T00:00:00Z"}):
        with fixture() as (_, snapshot):
            document = {"schema_version": "1.0", "generated_at": "2020-01-01T00:00:00Z", "issues": {"CON-1": "open"}, "linear_read_at": times}
            snapshot.write_text(json.dumps(document))
            findings = []
            assert coverage_state.load_issue_states(findings) is None and findings, (times, findings)


if __name__ == "__main__":
    failures = 0
    for name, fn in sorted(globals().copy().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"ok  {name}")
            except AssertionError as error:
                failures += 1
                print(f"FAIL {name}: {error}")
    sys.exit(1 if failures else 0)
