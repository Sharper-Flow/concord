#!/usr/bin/env python3
"""Tests for scripts/release-restore-gate.py.

The GitHub release population is injected as data, and `gh api`
is faked at the subprocess boundary by an executable on PATH. Writers and
candidates are small executables that read and write real SQLite files, so
snapshot, conservation, and effect checks run against real stores. No
published binary is downloaded and no installed store is reachable. The
population join has its own suite, test-release-restore-population.py.

The suite lowers its own hard file-size limit before any test runs, and every
child inherits it, so no test can write a file larger than FILE_LIMIT even when
a bound under test is broken. Growth fixtures also stop at GROWTH_CAP. Child
confinement has its own suite, test-release-restore-confine.py.
"""

from __future__ import annotations

import argparse
import dataclasses
import hashlib
import importlib.util
import json
import os
import resource
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("release_restore_gate", Path(__file__).with_name("release-restore-gate.py"))
assert SPEC and SPEC.loader
gate = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = gate
SPEC.loader.exec_module(gate)

from release_restore_invoke import invoke_plan, tag_facts

LEGACY_SPEC = importlib.util.spec_from_file_location("release_restore_legacy", Path(__file__).with_name("release_restore_legacy.py"))
assert LEGACY_SPEC and LEGACY_SPEC.loader
legacy = importlib.util.module_from_spec(LEGACY_SPEC)
sys.modules[LEGACY_SPEC.name] = legacy
LEGACY_SPEC.loader.exec_module(legacy)

REPO = Path(__file__).resolve().parents[1]


def plan_for(tag: str) -> tuple[str, ...]:
    """The invoke plan derived from this checkout's own tagged source, as the gate derives it."""
    return invoke_plan(tag_facts(REPO, tag))


TARGET = "a" * 40
FILE_LIMIT = 64 << 20
GROWTH_CAP = 8 << 20
resource.setrlimit(resource.RLIMIT_FSIZE, (FILE_LIMIT, FILE_LIMIT))

# A bounded runaway: grow HOME/growth to GROWTH_CAP, then idle, so a missing
# gate bound shows as a timeout, not a full disk.
GROW = """
def grow():
    import os, time
    with open(os.path.join(os.environ["HOME"], "growth"), "wb") as handle:
        while handle.tell() < {cap}:
            handle.write(b"x" * 65536)
            handle.flush()
    time.sleep(60)
""".format(cap=GROWTH_CAP).replace("{", "{{").replace("}", "}}")

WRITER = """#!{python}
import json, os, sqlite3, sys
TAG = {tag!r}
MODE = {mode!r}
""" + GROW + """
if sys.argv[1:] == ["--version"]:
    print("v0.0.0-wrong" if MODE == "wrong-version" else TAG)
    sys.exit(0)
with open(os.path.join(os.environ["HOME"], "env.json"), "w") as handle:
    json.dump(dict(os.environ), handle)
if MODE == "refuse":
    sys.exit(2)
if MODE == "grow":
    grow()
if MODE == "deaf":
    import time
    time.sleep(30)
request = json.load(sys.stdin)
db = sqlite3.connect(os.environ["CONCORD_DB_PATH"])
db.execute("CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)")
db.execute("INSERT OR IGNORE INTO schema_migrations VALUES (7)")
for table, key, subject in (("products", "id", "gate-product"), ("projects", "id", "gate-project"), ("managed_resources", "resource_id", "gate-resource")):
    db.execute(f"CREATE TABLE IF NOT EXISTS {{table}} ({{key}} TEXT PRIMARY KEY, display_name TEXT, version INTEGER)")
    db.execute(f"INSERT INTO {{table}} ({{key}}, version) VALUES (?, 1) ON CONFLICT({{key}}) DO UPDATE SET version = version + 1", (subject,))
db.execute("UPDATE products SET display_name='Gate Product' WHERE id='gate-product'")
db.execute("UPDATE projects SET display_name='Gate Project' WHERE id='gate-project'")
db.execute("CREATE TABLE IF NOT EXISTS product_projects (product_id TEXT, project_id TEXT, role TEXT, PRIMARY KEY(product_id, project_id))")
db.execute("INSERT OR IGNORE INTO product_projects VALUES ('gate-product', 'gate-project', 'primary')")
db.execute("CREATE TABLE IF NOT EXISTS project_locators (project_id TEXT, locator_id TEXT, kind TEXT, locator_value TEXT, PRIMARY KEY(project_id, locator_id))")
db.execute("INSERT OR IGNORE INTO project_locators VALUES ('gate-project', 'gate-remote', 'git_remote', 'https://example.invalid/gate/project.git')")
if MODE == "missing-current-effect":
    db.execute("DELETE FROM project_locators")
db.commit()
if sys.argv[1] == "grant":
    print(json.dumps({{"ok": True, "grant_token": "writer-token", "scope_version": "sha256:" + "0" * 64,
                       "surface_version": "4.0.0", "envelope_version": "1.0"}}))
    sys.exit(0)
if sys.argv[1] == "invoke":
    operation = request["operation"]
    db.execute("CREATE TABLE IF NOT EXISTS work_items (id TEXT PRIMARY KEY, version INTEGER, title TEXT, lifecycle TEXT)")
    db.execute("CREATE TABLE IF NOT EXISTS work_messages (message_id TEXT PRIMARY KEY, sender_work_id TEXT, recipient_work_id TEXT, body TEXT, state TEXT)")
    db.execute("CREATE TABLE IF NOT EXISTS resource_claims (resource_key TEXT PRIMARY KEY, holder_work_id TEXT, holder_agent TEXT, holder_session TEXT, reason TEXT, state TEXT)")
    db.execute("CREATE TABLE IF NOT EXISTS work_observations (observation_id TEXT PRIMARY KEY, work_id TEXT, statement TEXT,"
               " refs TEXT CHECK(json_type(refs)='array'), tags TEXT CHECK(json_type(tags)='array'))")
    if operation == "capture":
        n = db.execute("SELECT COUNT(*) FROM work_items").fetchone()[0] + 1
        work_id = "gate-work-" + str(n)
        db.execute("INSERT INTO work_items VALUES (?, 2, ?, 'needed')", (work_id, request["input"]["title"]))
        refs = [{{"entity_kind": "work_item", "id": work_id, "version": "2"}}]
    elif operation == "message_send":
        inp = request["input"]
        sender = db.execute("SELECT version FROM work_items WHERE id=?", (inp["work_id"],)).fetchone()
        assert sender and sender[0] == inp["expected_version"], "sender version must come from the capture's changed refs"
        assert inp["recipient_work_id"] != inp["work_id"], "self-send refuses"
        assert db.execute("SELECT 1 FROM work_items WHERE id=?", (inp["recipient_work_id"],)).fetchone(), "recipient must exist"
        db.execute("UPDATE work_items SET version=? WHERE id=?", (inp["expected_version"] + 1, inp["work_id"]))
        db.execute("INSERT INTO work_messages VALUES (?, ?, ?, ?, 'sent')",
                   ("msg:" + format(inp["expected_version"], "032x"), inp["work_id"], inp["recipient_work_id"], inp["body"]))
        refs = [{{"entity_kind": "work_item", "id": inp["work_id"], "version": str(inp["expected_version"] + 1)}}]
    elif operation == "message_withdraw":
        inp = request["input"]
        state = db.execute("SELECT state FROM work_messages WHERE message_id=?", (inp["message_id"],)).fetchone()
        assert state == ("sent",), "withdrawal needs the exact sent message"
        sender = db.execute("SELECT version FROM work_items WHERE id=?", (inp["work_id"],)).fetchone()
        assert sender and sender[0] == inp["expected_version"], "withdraw version must come from the send's changed refs"
        db.execute("UPDATE work_items SET version=? WHERE id=?", (inp["expected_version"] + 1, inp["work_id"]))
        db.execute("UPDATE work_messages SET state='withdrawn' WHERE message_id=?", (inp["message_id"],))
        refs = [{{"entity_kind": "work_item", "id": inp["work_id"], "version": str(inp["expected_version"] + 1)}}]
    elif operation == "resource_claim":
        inp = request["input"]
        current = db.execute("SELECT version FROM work_items WHERE id=?", (inp["work_id"],)).fetchone()
        assert current and current[0] == inp["expected_version"], "claim needs the work item's current version"
        db.execute("UPDATE work_items SET version=? WHERE id=?", (inp["expected_version"] + 1, inp["work_id"]))
        db.execute("INSERT INTO resource_claims VALUES (?, ?, ?, ?, ?, 'held')",
                   (inp["resource_key"], inp["work_id"], request["call_envelope"]["agent_ref"], request["call_envelope"]["session_ref"], inp["reason"]))
        carrier = ("work_item", inp["work_id"]) if MODE == "current-resource-ref" else ("resource_claim", inp["resource_key"])
        refs = [{{"entity_kind": carrier[0], "id": carrier[1], "version": str(inp["expected_version"] + 1)}}]
    elif operation == "resource_release":
        inp = request["input"]
        current = db.execute("SELECT version FROM work_items WHERE id=?", (inp["work_id"],)).fetchone()
        held = db.execute("SELECT state FROM resource_claims WHERE resource_key=?", (inp["resource_key"],)).fetchone()
        assert current and current[0] == inp["expected_version"], "release needs the work item's current version"
        assert held == ("held",), "release needs the held claim"
        db.execute("UPDATE work_items SET version=? WHERE id=?", (inp["expected_version"] + 1, inp["work_id"]))
        if MODE != "held-resource-projection":
            db.execute("UPDATE resource_claims SET state='released' WHERE resource_key=?", (inp["resource_key"],))
        carrier = ("work_item", inp["work_id"]) if MODE == "current-resource-ref" else ("resource_claim", inp["resource_key"])
        refs = [{{"entity_kind": carrier[0], "id": carrier[1], "version": str(inp["expected_version"] + 1)}}]
    elif operation == "observation_record":
        inp = request["input"]
        # The v0.31.0 fold serialized omitted lists to JSON null against array
        # CHECKs and reported the INSERT failure as a duplicate id: keep that
        # contract so only a recipe sending explicit lists can pass.
        db.execute("INSERT INTO work_observations VALUES (?, ?, ?, ?, ?)",
                   (inp["observation_id"], inp["work_id"], inp["statement"], json.dumps(inp.get("refs")), json.dumps(inp.get("tags"))))
        refs = [{{"entity_kind": "observation", "id": inp["observation_id"], "version": "1"}}]
    else:
        print(json.dumps({{"outcome": "error", "error": {{"message": "unknown operation " + operation}}}}))
        sys.exit(1)
    db.commit()
    if MODE == "bad-resource-ref" and operation == "resource_claim":
        refs[0]["version"] = int(refs[0]["version"])
    if MODE == "capture-marshal-error" and operation == "capture":
        print("json: error calling MarshalJSON for type agent.Envelope: invalid result payload: type mismatch at $.changed_refs[0].version", file=sys.stderr)
        sys.exit(1)
    print(json.dumps({{"outcome": "ok", "changed_refs": refs}}))
    sys.exit(0)
print(json.dumps({{"ok": True}}))
"""

CANDIDATE = """#!{python}
import hashlib, json, sqlite3, sys
VERSION = {version!r}
MODE = {mode!r}
""" + GROW + """
if sys.argv[1:] == ["--version"]:
    if MODE == "grow-preflight":
        grow()
    print(VERSION)
    sys.exit(0)
if MODE == "refuse":
    sys.exit(1)
if MODE == "self-modify":
    open(__file__, "a").write("\\n")
request = json.load(sys.stdin)
manifest = json.load(open(request["source"] + ".manifest.json"))
second = manifest["binary_version"].endswith("-restored")
if MODE == "mutate-source":
    open(request["source"], "ab").write(b"x")
source = sqlite3.connect("file:" + request["source"] + "?mode=ro", uri=True)
target = sqlite3.connect(request["destination"])
source.backup(target)
source.close()
if MODE == "lose-resource-effect":
    target.execute("UPDATE resource_claims SET state='held'")
if MODE == "rebuild-resource-effect" and second:
    target.execute("UPDATE resource_claims SET state='held'")
if MODE == "lose-effect":
    target.execute("UPDATE project_locators SET locator_value='https://example.invalid/other.git'")
if MODE == "lose-work" or (MODE == "second-lose-work" and second):
    target.execute("DELETE FROM work_items WHERE id='gate-work-1'")
if MODE == "lose-message" or (MODE == "second-lose-message" and second):
    target.execute("UPDATE work_messages SET state='sent'")
target.commit()
schema = target.execute("SELECT MAX(version) FROM schema_migrations").fetchone()[0]
target.close()
checksum = hashlib.sha256(open(request["destination"], "rb").read()).hexdigest()
inventory = {{"schema_version": schema, "db_checksum": checksum}}
if MODE == "wrong-source":
    manifest["db_checksum"] = "0" * 64
if MODE == "wrong-snapshot-id":
    manifest["snapshot_id"] = "other"
if MODE == "wrong-destination":
    inventory["db_checksum"] = "0" * 64
if MODE == "missing-inventory":
    inventory = {{}}
if MODE == "malformed-report":
    print("{{")
    sys.exit(0)
print(json.dumps({{"source_snapshot": manifest, "restored_database": inventory}}))
"""

FAKE_GH = """#!{python}
import sys
from pathlib import Path
asset = Path({assets!r}) / sys.argv[-1].rsplit("/", 1)[-1]
if not asset.exists():
    sys.exit(1)
sys.stdout.buffer.write(asset.read_bytes())
"""


def release_of(writer: gate.Writer, release_id: int) -> dict:
    return {"id": release_id, "tag_name": writer.tag, "draft": False, "assets": [
        {"id": writer.asset_id, "name": f"concord-{writer.tag}", "size": writer.asset_size, "digest": writer.asset_digest},
        {"id": writer.checksum_asset_id, "name": f"concord-{writer.tag}.sha256", "size": writer.checksum_size}]}


def tag_map(*tags: str) -> dict:
    return {tag: (f"{index:040x}", f"{index + 100:040x}") for index, tag in enumerate(tags, start=1)}


class Fixture:
    """Fake published assets, a fake gh, writers, and candidates under one root."""

    def __init__(self, root: Path) -> None:
        self.root = root
        self.assets = root / "assets"
        self.next_id = 0
        (root / "bin").mkdir(parents=True)
        self.write(root / "bin" / "gh", FAKE_GH.format(python=sys.executable, assets=str(self.assets)))

    @staticmethod
    def write(path: Path, text: str) -> Path:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        path.chmod(0o755)
        return path

    def publish(self, tag: str, mode: str = "ok", corrupt: bool = False, size_delta: int = 0) -> gate.Writer:
        self.next_id += 2
        binary = self.write(self.assets / str(self.next_id), WRITER.format(python=sys.executable, tag=tag, mode=mode))
        digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        checksum = self.assets / str(self.next_id + 1)
        checksum.write_text(f"{'0' * 64 if corrupt else digest}  concord-{tag}\n", encoding="utf-8")
        commit = subprocess.run(["git", "-C", str(REPO), "rev-parse", f"{tag}^{{commit}}"],
                                capture_output=True, text=True, check=True).stdout.strip()
        return gate.Writer(tag, 1, "1" * 40, commit, self.next_id, binary.stat().st_size + size_delta, f"sha256:{digest}",
                           self.next_id + 1, checksum.stat().st_size)

    def candidate(self, mode: str = "ok", version: str = "v9.0.0") -> Path:
        return self.write(self.root / f"candidate-{mode}", CANDIDATE.format(python=sys.executable, version=version, mode=mode))


class CaseTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.fixture = Fixture(Path(self.tempdir.name))
        path = f"{self.fixture.root / 'bin'}{os.pathsep}{os.environ['PATH']}"
        env = {"PATH": path, "CONCORD_DB_PATH": "/live/concord.db", "GH_TOKEN": "synthetic-token"}
        self.env = patch.dict(os.environ, env)
        self.env.start()

    def tearDown(self) -> None:
        self.env.stop()
        self.tempdir.cleanup()

    def case(self, writer: gate.Writer, candidate: Path, case_seconds: int = 60, scratch: gate.Scratch | None = None,
              transfer: int = 1 << 30) -> dict:
        work = self.fixture.root / "work"
        return gate.run_writer_case("owner/repo", writer, candidate, work, work / "streams" / writer.tag, case_seconds,
                                    time.monotonic() + 600, scratch or gate.Scratch(1 << 30, 1), gate.Transfer(transfer))

    def assertCase(self, row: dict, status: str, stage: str) -> None:
        self.assertEqual((row["status"], row["stage"]), (status, stage), row)

    def test_a_writer_restores_twice_and_its_case_is_removed(self) -> None:
        row = self.case(self.fixture.publish("v1.0.0"), self.fixture.candidate())
        self.assertCase(row, "pass", "complete")
        self.assertEqual((row["source"]["schema_version"], row["restored"]["schema_version"], row["restored_again"]["schema_version"]), (7, 7, 7))
        for inventory in (row["source"], row["restored"], row["restored_again"]):
            self.assertRegex(inventory["db_checksum"], r"^[0-9a-f]{64}$")
        self.assertEqual(row["invoke"]["plan"], list(plan_for("v1.0.0")))
        self.assertFalse((self.fixture.root / "work" / "v1.0.0").exists())

    def test_current_effects_survive_both_restores_without_history(self) -> None:
        real_effects = gate.effects
        checked = []

        def logless_effects(path, deadline):
            with sqlite3.connect(f"file:{path}?mode=ro", uri=True) as db:
                self.assertIsNone(db.execute("SELECT 1 FROM sqlite_schema WHERE name='domain_events'").fetchone())
            checked.append(path.name)
            return real_effects(path, deadline)

        with patch.object(gate, "effects", logless_effects):
            row = self.case(self.fixture.publish("v1.0.0"), self.fixture.candidate())
        self.assertCase(row, "pass", "complete")
        self.assertEqual(checked, ["snapshot.db", "restored.db", "restored-again.db"])
        for phase in ("source", "restored", "restored_again"):
            self.assertNotIn("event_watermark", row[phase])

    def test_a_passing_case_binds_writer_recipe_and_snapshot_provenance(self) -> None:
        writer = self.fixture.publish("v1.0.0")
        row = self.case(writer, self.fixture.candidate())
        self.assertCase(row, "pass", "complete")
        self.assertEqual(row["commit"], writer.commit)
        self.assertEqual(row["source"]["asset_digest"], writer.asset_digest)
        self.assertEqual(row["recipe_sha256"], gate.recipe_sha256(gate.recipe_for(writer.tag), plan_for(writer.tag)))
        self.assertRegex(row["source"]["db_checksum"], r"^[0-9a-f]{64}$")
        self.assertNotIn("witnesses", row)

    def test_recipe_steps_start_at_their_release(self) -> None:
        verbs = lambda tag: [verb for verb, _ in gate.recipe_for(tag)]
        self.assertEqual(verbs("v0.37.1"), ["product-create", "project-locator-add"])
        self.assertNotIn("product-knowledge-home-designate", verbs("v4.2.9"))
        self.assertIn("product-knowledge-home-designate", verbs("v4.3.0"))
        self.assertNotIn("product-knowledge-source-register", verbs("v11.58.9"))
        self.assertEqual(len(verbs("v11.59.0")), len(gate.RECIPE))
        plan = plan_for("v4.5.0")
        self.assertNotEqual(gate.recipe_sha256(gate.recipe_for("v4.3.0"), plan), gate.recipe_sha256(gate.recipe_for("v4.7.0"), plan))

    def test_the_recipe_hash_binds_the_actual_phase_plan_variant(self) -> None:
        steps = gate.recipe_for("v1.0.0")
        message_plan = plan_for("v1.0.0")
        capture_only = ("client-register", "grant", "invoke:concord_work_define.capture")
        no_grant = message_plan[:1] + message_plan[2:]
        self.assertNotEqual(gate.recipe_sha256(steps, capture_only), gate.recipe_sha256(steps, message_plan))
        self.assertNotEqual(gate.recipe_sha256(steps, message_plan), gate.recipe_sha256(steps, no_grant))
        self.assertEqual(gate.recipe_sha256(steps, message_plan), gate.recipe_sha256(steps, plan_for("v1.0.0")))

    def test_a_capture_only_era_reports_only_its_executed_steps(self) -> None:
        row = self.case(self.fixture.publish("v0.8.0"), self.fixture.candidate())
        self.assertCase(row, "pass", "complete")
        plan = plan_for("v0.8.0")
        self.assertEqual(plan, ("client-register", "grant", "invoke:concord_work_define.capture"))
        self.assertEqual(row["invoke"]["plan"], list(plan))
        self.assertNotEqual(row["recipe_sha256"], gate.recipe_sha256(gate.recipe_for("v0.8.0"), plan_for("v1.0.0")))

    def test_resources_run_before_messages_exist(self) -> None:
        row = self.case(self.fixture.publish("v0.26.1"), self.fixture.candidate())
        self.assertCase(row, "pass", "complete")
        self.assertEqual(row["invoke"]["plan"], ["client-register", "grant", "invoke:concord_work_define.capture",
                                                "invoke:concord_work_relate.resource_claim", "invoke:concord_work_relate.resource_release"])

    def test_current_resource_receipts_are_checked_without_relabeling(self) -> None:
        row = self.case(self.fixture.publish("v1.0.0", mode="current-resource-ref"), self.fixture.candidate())
        self.assertCase(row, "pass", "complete")

    def test_writer_current_effect_checks_precede_admission(self) -> None:
        for tag, mode, stage in (("v0.37.2", "missing-current-effect", "snapshot-effects"),
                                 ("v0.39.0", "held-resource-projection", "invoke-snapshot-effects"),
                                 ("v0.40.0", "bad-resource-ref", "invoke-resource-claim")):
            with self.subTest(mode=mode):
                row = self.case(self.fixture.publish(tag, mode=mode), self.fixture.candidate())
                self.assertCase(row, "unknown", stage)
                self.assertNotIn("invoke", row)
                if mode == "bad-resource-ref":
                    store = gate.sandbox_env(self.fixture.root / "work" / tag)["CONCORD_DB_PATH"]
                    db = sqlite3.connect(f"file:{store}?mode=ro", uri=True)
                    self.addCleanup(db.close)
                    self.assertEqual(db.execute("SELECT state FROM resource_claims").fetchall(), [("held",)])

    def test_both_restores_must_preserve_resource_effects(self) -> None:
        for tag, mode, stage in (("v0.37.2", "lose-resource-effect", "effects"),
                                 ("v0.39.0", "rebuild-resource-effect", "restore-again-effects")):
            with self.subTest(mode=mode):
                row = self.case(self.fixture.publish(tag), self.fixture.candidate(mode))
                self.assertCase(row, "fail", stage)

    def test_both_restores_must_preserve_work_and_message_effects(self) -> None:
        for tag, mode, stage in (("v1.0.0", "lose-work", "effects"),
                                 ("v1.0.1", "second-lose-work", "restore-again-effects"),
                                 ("v1.0.2", "lose-message", "effects"),
                                 ("v1.1.0", "second-lose-message", "restore-again-effects")):
            with self.subTest(mode=mode):
                self.assertCase(self.case(self.fixture.publish(tag), self.fixture.candidate(mode)), "fail", stage)

    def test_a_corrupt_snapshot_is_refused_before_restore_execution(self) -> None:
        real = gate.snapshot_fixture

        def corrupt(*args):
            manifest = real(*args)
            with args[1].open("ab") as handle:
                handle.write(b"corrupt")
            return manifest

        with patch.object(gate, "snapshot_fixture", corrupt):
            row = self.case(self.fixture.publish("v1.0.0"), self.fixture.candidate())
        self.assertCase(row, "fail", "restore")
        self.assertIn("checksum", row["detail"])
        self.assertFalse((self.fixture.root / "work" / "v1.0.0" / "restored.db").exists())

    def test_a_missing_versioned_subject_is_a_named_recipe_failure(self) -> None:
        store = self.fixture.root / "empty.db"
        sqlite3.connect(store).execute("CREATE TABLE products (id TEXT, version INTEGER)").connection.commit()
        with self.assertRaises(gate.CaseFailure) as raised:
            gate.resolve({"expected_version": gate.PRODUCT_VERSION}, store, self.fixture.root, time.monotonic() + 10)
        self.assertEqual((raised.exception.status, raised.exception.stage), ("unknown", "recipe"))
        resolved = gate.resolve({"value": gate.repository("home")}, store, self.fixture.root, time.monotonic() + 10)
        self.assertTrue(Path(resolved["value"]).is_dir())

    def test_a_capture_output_failure_keeps_a_capture_plan_writer_held(self) -> None:
        # The fake represents a capture-plan writer (v0.26.1 selects capture with
        # grant) whose capture output cannot marshal: the writer stays held, the
        # refusal is named, and nothing is salvaged into a success or a skip.
        row = self.case(self.fixture.publish("v0.26.1", mode="capture-marshal-error"), self.fixture.candidate())
        self.assertCase(row, "unknown", "invoke-invoke")
        self.assertIn("capture exited 1: json: error calling MarshalJSON for type agent.Envelope: invalid result payload: type mismatch at $.changed_refs[0].version", row["detail"])
        self.assertNotIn("invoke", row)

    def test_tree_bytes_counts_a_journal_the_writer_deletes_mid_walk_as_zero(self) -> None:
        # The confined writer may delete its SQLite journal between is_file()
        # and stat(); the walk must count the remaining tree instead of failing
        # the whole case at the harness stage.
        root = self.fixture.root / "journal-race"
        (root / "store").mkdir(parents=True)
        journal = root / "store" / "concord.db-journal"
        journal.write_bytes(b"j" * 4096)
        (root / "keep.db").write_bytes(b"k" * 1024)
        real_stat, real_is_file = Path.stat, Path.is_file

        def vanished_after_the_existence_check(path, **flags):
            if path == journal:
                raise FileNotFoundError(2, "No such file or directory", str(path))
            return real_stat(path, **flags)

        with patch.object(Path, "is_file", lambda path: True if path == journal else real_is_file(path)), \
                patch.object(Path, "stat", vanished_after_the_existence_check):
            self.assertEqual(gate.tree_bytes(root), 1024)

    def test_gate_run_binds_the_legacy_remote_commit_for_its_cases(self) -> None:
        # The gate derives each case's remote commit from the enumerated tag map,
        # as a live run does, so the selection inside run_writer_case binds it.
        commit = subprocess.run(["git", "-C", str(REPO), "rev-parse", "v0.1.0^{commit}"],
                                capture_output=True, text=True, check=True).stdout.strip()
        writer = self.fixture.publish("v0.1.0")
        releases = [release_of(writer, 1)]
        report, _ = gate.run_gate(self.args(self.fixture.candidate()), releases, {"v0.1.0": ("0" * 40, commit)})
        row = report["rows"][0]
        self.assertEqual((row["status"], row["tag"]), ("pass", "v0.1.0"))
        self.assertNotIn("invoke", row)
        self.assertEqual(row["source_proof"]["variant"], "legacy-bootstrap")
        self.assertEqual(row["source_proof"]["commit"], commit)

    def test_a_proven_legacy_writer_runs_the_bootstrap_pair_only(self) -> None:
        # The fake emulates the v0.1.0 writer's operator surface: the product-create
        # and project-locator-add pair writes only operator fixture rows and no
        # client/grant/invoke verb is ever sent to it.
        commit = subprocess.run(["git", "-C", str(REPO), "rev-parse", "v0.1.0^{commit}"],
                                capture_output=True, text=True, check=True).stdout.strip()
        row = self.case(self.fixture.publish("v0.1.0"), self.fixture.candidate())
        self.assertCase(row, "pass", "complete")
        self.assertNotIn("invoke", row)
        proof = row["source_proof"]
        self.assertEqual(proof["variant"], "legacy-bootstrap")
        self.assertEqual(proof["commit"], commit)
        self.assertEqual(row["commit"], proof["commit"])
        expected = {path: hashlib.sha256(subprocess.run(["git", "-C", str(REPO), "show", f"v0.1.0:{path}"],
                                                        capture_output=True, check=True).stdout).hexdigest()
                    for path in legacy.LEGACY_OWNERS}
        self.assertEqual(proof["owners"], expected)
        self.assertEqual(proof["fingerprint"], legacy.fingerprint_of(proof["owners"]))
        steps = gate.recipe_for("v0.1.0")
        self.assertEqual(row["recipe_sha256"], gate.recipe_sha256(steps, ()))
        self.assertNotEqual(row["recipe_sha256"], gate.recipe_sha256(steps, ("client-register", "grant", "invoke:concord_work_define.capture")))
        self.assertEqual(row["source"]["asset_digest"], row["asset_digest"])

    def test_an_unbound_remote_commit_holds_a_legacy_writer(self) -> None:
        # A v0.1.0 release whose remote commit does not bind the pinned source
        # commit must never run bootstrap-only or capture: the writer stays unevaluated.
        writer = dataclasses.replace(self.fixture.publish("v0.1.0"), commit="1" * 40)
        row = self.case(writer, self.fixture.candidate())
        self.assertCase(row, "unknown", "invoke-legacy-proof")
        self.assertIn("does not bind the pinned source commit", row["detail"])
        self.assertNotIn("invoke", row)
        self.assertNotIn("source_proof", row)

    def test_the_writer_identity_cannot_be_replaced_by_a_separate_commit(self) -> None:
        writer = dataclasses.replace(self.fixture.publish("v0.1.0"), commit="1" * 40)
        row = self.case(writer, self.fixture.candidate())
        self.assertCase(row, "unknown", "invoke-legacy-proof")
        self.assertNotIn("source_proof", row)

    def test_the_empty_legacy_plan_hashes_distinctly(self) -> None:
        steps = gate.recipe_for("v0.1.0")
        legacy_hash = gate.recipe_sha256(steps, ())
        capture_only = gate.recipe_sha256(steps, ("client-register", "grant", "invoke:concord_work_define.capture"))
        messages = gate.recipe_sha256(steps, ("client-register", "grant", "invoke:concord_work_define.capture",
                                              "invoke:concord_work_define.capture", "invoke:concord_work_relate.message_send",
                                              "invoke:concord_work_relate.message_withdraw"))
        no_grant = gate.recipe_sha256(steps, ("client-register", "invoke:concord_work_define.capture"))
        self.assertEqual(len({legacy_hash, capture_only, messages, no_grant}), 4)

    def test_writer_environment_is_constructed(self) -> None:
        self.case(self.fixture.publish("v1.0.0", mode="refuse"), self.fixture.candidate())
        case = self.fixture.root / "work" / "v1.0.0"
        seen = json.loads((case / "home" / "env.json").read_text())
        self.assertEqual(seen["CONCORD_DB_PATH"], str(case / "store" / "concord.db"))
        self.assertNotIn("GH_TOKEN", seen)
        self.assertEqual(seen["PATH"], "/usr/bin:/bin")

    def test_artifact_and_writer_failures_are_named(self) -> None:
        candidate = self.fixture.candidate()
        self.assertCase(self.case(self.fixture.publish("v1.0.0", corrupt=True), candidate), "fail", "checksum")
        self.assertCase(self.case(self.fixture.publish("v1.0.1", mode="wrong-version"), candidate), "fail", "version")
        self.assertCase(self.case(self.fixture.publish("v1.0.2", mode="refuse"), candidate), "unknown", "product-create")
        self.assertCase(self.case(self.fixture.publish("v1.1.3", size_delta=1), candidate), "fail", "download")
        replaced = dataclasses.replace(self.fixture.publish("v1.1.4"), asset_id=999)
        self.assertCase(self.case(replaced, candidate), "unknown", "download")

    def test_candidate_failures_are_named(self) -> None:
        tags = ("v1.0.0", "v1.0.1", "v1.0.2", "v1.1.0", "v1.1.1", "v1.1.2", "v1.1.3", "v1.1.4")
        for tag, (mode, stage) in zip(tags, (("refuse", "restore"), ("mutate-source", "conservation"),
                            ("lose-effect", "effects"), ("wrong-source", "restore"),
                            ("wrong-snapshot-id", "restore"), ("wrong-destination", "restore"),
                            ("missing-inventory", "restore"), ("malformed-report", "harness"))):
            with self.subTest(mode=mode):
                row = self.case(self.fixture.publish(tag), self.fixture.candidate(mode))
                self.assertCase(row, "unknown" if stage == "harness" else "fail", stage)

    def test_resource_budgets_stop_the_case(self) -> None:
        candidate = self.fixture.candidate()
        self.assertCase(self.case(self.fixture.publish("v1.0.0"), candidate, transfer=10), "unknown", "transfer")
        self.assertCase(self.case(self.fixture.publish("v1.0.1"), candidate, scratch=gate.Scratch(10, 1)), "unknown", "disk")

    def test_scratch_growth_stops_a_running_writer(self) -> None:
        scratch = gate.Scratch(4 << 20, 1)
        started = time.monotonic()
        row = self.case(self.fixture.publish("v1.0.0", mode="grow"), self.fixture.candidate(), scratch=scratch)
        self.assertCase(row, "unknown", "disk")
        self.assertLess(time.monotonic() - started, 20)
        self.assertGreater(scratch.retained, 0)
        self.assertLess(scratch.allowance(), 4 << 20)

    def test_a_writer_that_never_reads_stdin_meets_its_budget(self) -> None:
        started = time.monotonic()
        row = self.case(self.fixture.publish("v1.0.0", mode="deaf"), self.fixture.candidate(), case_seconds=2)
        self.assertCase(row, "unknown", "step-0-product-create")
        self.assertLess(time.monotonic() - started, 10)

    def test_exhausted_wall_budget_is_unknown(self) -> None:
        writer = self.fixture.publish("v1.0.0")
        work = self.fixture.root / "work"
        with patch.object(gate, "tag_facts", side_effect=AssertionError("expired cases must not read source")) as facts:
            row = gate.run_writer_case("owner/repo", writer, self.fixture.candidate(), work, work / "s", 60, time.monotonic() - 1,
                                       gate.Scratch(1 << 30, 1), gate.Transfer(1 << 30))
        facts.assert_not_called()
        self.assertCase(row, "unknown", "wall")

    def args(self, candidate: Path, version: str = "v9.0.0") -> argparse.Namespace:
        return argparse.Namespace(repository="owner/repo", target_sha=TARGET, candidate=str(candidate), candidate_version=version,
                                  output_dir=str(self.fixture.root / "out"), workers=2, wall_seconds=600, case_seconds=60,
                                  disk_bytes=1 << 30, transfer_bytes=1 << 30)

    def gate_run(self, candidate: Path, *writers: gate.Writer) -> tuple[dict, int]:
        releases = [release_of(writer, index) for index, writer in enumerate(writers, start=1)]
        return gate.run_gate(self.args(candidate), releases, tag_map(*(writer.tag for writer in writers)))

    def test_gate_passes_when_every_writer_passes(self) -> None:
        report, status = self.gate_run(self.fixture.candidate(), self.fixture.publish("v1.0.0"), self.fixture.publish("v1.1.2"))
        self.assertEqual((status, report["result"]), (0, "pass"))
        self.assertNotIn("coverage", report)
        self.assertEqual([row["tag"] for row in report["rows"]], ["v1.0.0", "v1.1.2"])
        self.assertEqual(report["population"]["writers"], 2)
        self.assertGreater(report["budgets"]["transferred_bytes"], 0)
        self.assertFalse((self.fixture.root / "out" / "witnesses.jsonl").exists())

    def test_gate_fails_on_any_writer_or_empty_population(self) -> None:
        report, status = self.gate_run(self.fixture.candidate(), self.fixture.publish("v1.0.0"), self.fixture.publish("v1.1.2", corrupt=True))
        self.assertEqual((status, report["result"], report["counts"]), (1, "fail", {"pass": 1, "fail": 1, "unknown": 0}))
        report, status = gate.run_gate(self.args(self.fixture.candidate()), [], {})
        self.assertEqual((status, report["result"]), (1, "unknown"))

    def test_missing_writer_provenance_does_not_pass_the_population(self) -> None:
        writers = (self.fixture.publish("v1.0.0"), self.fixture.publish("v1.1.2"))
        releases = [release_of(writer, index) for index, writer in enumerate(writers, start=1)]
        releases[1]["assets"].pop()
        report, status = gate.run_gate(self.args(self.fixture.candidate()), releases, tag_map(*(writer.tag for writer in writers)))
        self.assertEqual((status, report["result"], report["counts"]), (1, "unknown", {"pass": 1, "fail": 0, "unknown": 1}))
        self.assertEqual(report["population"]["writers"], 2)

    def test_preflight_bounds_scratch_and_the_wall(self) -> None:
        args = self.args(self.fixture.candidate("grow-preflight"))
        args.disk_bytes = 1 << 20
        with self.assertRaisesRegex(gate.GateError, "preflight exceeds its scratch"):
            gate.run_gate(args, [], {})
        with self.assertRaisesRegex(gate.GateError, "during enumeration"):
            gate.run_gate(self.args(self.fixture.candidate()), [], {}, time.monotonic() - 1)

    def test_snapshot_work_stops_at_the_deadline_and_on_a_signal(self) -> None:
        store = self.fixture.root / "store.db"
        db = sqlite3.connect(store)
        db.execute("CREATE TABLE current_effects (id INTEGER PRIMARY KEY, state TEXT)")
        db.executemany("INSERT INTO current_effects (state) VALUES (?)", [("x" * 4000,)] * 2000)
        db.commit()
        db.close()
        with self.assertRaisesRegex(gate.CaseFailure, "budget exhausted"):
            gate.snapshot_fixture(store, self.fixture.root / "late.db", "v1.0.0", deadline=time.monotonic() - 1)
        self.assertLess((self.fixture.root / "late.db").stat().st_size, store.stat().st_size, "the backup ran past its deadline")
        gate.STOP.set()
        try:
            with self.assertRaisesRegex(gate.CaseFailure, "stopped by a signal"):
                gate.snapshot_fixture(store, self.fixture.root / "stopped.db", "v1.0.0")
        finally:
            gate.STOP.clear()

    def test_snapshot_refuses_a_store_beyond_the_remaining_scratch(self) -> None:
        store = self.fixture.root / "store.db"
        store.write_bytes(b"x" * 4096)
        with self.assertRaisesRegex(gate.CaseFailure, "exceeds the case's remaining scratch"):
            gate.snapshot_fixture(store, self.fixture.root / "snap.db", "v1.0.0", room=lambda: 100)

    def test_population_reads_run_confined_bounded_and_interruptible(self) -> None:
        page = [{"id": 1, "tag_name": "v1.0.0"}]
        (self.fixture.assets / "releases?per_page=100&page=1").parent.mkdir(parents=True, exist_ok=True)
        (self.fixture.assets / "releases?per_page=100&page=1").write_text(json.dumps(page))
        streams = self.fixture.root / "out" / "streams" / "population"
        run = gate.population_runner(streams, time.monotonic() + 60, gate.Scratch(1 << 30, 1))
        self.assertEqual(gate.gh_release_page("owner/repo", 1, run), page)
        with self.assertRaisesRegex(gate.GateError, "exited 1"):
            gate.gh_release_page("owner/repo", 2, run)
        with self.assertRaisesRegex(gate.GateError, "budget exhausted"):
            gate.gh_release_page("owner/repo", 1, gate.population_runner(streams, time.monotonic() - 1, gate.Scratch(1 << 30, 1)))
        with self.assertRaisesRegex(gate.GateError, "population reads exceed"):
            gate.gh_release_page("owner/repo", 1, gate.population_runner(streams, time.monotonic() + 60, gate.Scratch(16, 1)))
        gate.STOP.set()
        try:
            with self.assertRaisesRegex(gate.GateError, "stopped by a signal"):
                gate.gh_release_page("owner/repo", 1, run)
        finally:
            gate.STOP.clear()

    def test_candidate_identity_is_bound(self) -> None:
        with self.assertRaisesRegex(gate.GateError, "candidate reports"):
            gate.run_gate(self.args(self.fixture.candidate(version="v8.0.0")), [], {})
        with self.assertRaisesRegex(gate.GateError, "changed during the run"):
            self.gate_run(self.fixture.candidate("self-modify"), self.fixture.publish("v1.0.0"))
        self.assertFalse((self.fixture.root / "out" / "witnesses.jsonl").exists())

    def test_main_refuses_bad_identity_and_reused_output(self) -> None:
        output = self.fixture.root / "used"
        (output / "old").mkdir(parents=True)
        common = ["run", "--repository", "owner/repo", "--candidate", "x", "--candidate-version", "v9.0.0"]
        self.assertEqual(gate.main([*common, "--target-sha", TARGET, "--output-dir", str(output)]), 1)
        self.assertIn("not empty", json.loads((output / "report.json").read_text())["detail"])
        fresh = self.fixture.root / "fresh"
        self.assertEqual(gate.main([*common, "--target-sha", "abc", "--output-dir", str(fresh)]), 1)
        self.assertEqual(json.loads((fresh / "report.json").read_text())["result"], "unknown")


class SnapshotTests(unittest.TestCase):
    def test_snapshot_does_not_require_history(self) -> None:
        for history in (False, True):
            with self.subTest(history=history), tempfile.TemporaryDirectory() as directory:
                store = Path(directory) / "store.db"
                db = sqlite3.connect(store)
                db.execute("CREATE TABLE schema_migrations (version INTEGER)")
                db.execute("INSERT INTO schema_migrations VALUES (7)")
                db.execute("CREATE TABLE current_effects (id TEXT, state TEXT)")
                db.execute("INSERT INTO current_effects VALUES ('attempt', 'in_flight')")
                if history:
                    db.execute("CREATE TABLE domain_events (seq INTEGER PRIMARY KEY)")
                db.commit()
                db.close()
                snapshot = Path(directory) / "snap.db"
                manifest = gate.snapshot_fixture(store, snapshot, "v1.0.0")
                self.assertEqual(manifest["db_checksum"], gate.sha256_file(snapshot))
                restored = sqlite3.connect(snapshot)
                try:
                    self.assertEqual(restored.execute("SELECT * FROM current_effects").fetchall(), [("attempt", "in_flight")])
                finally:
                    restored.close()

    def test_corrupt_input_is_not_a_snapshot(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = Path(directory) / "store.db"
            store.write_bytes(b"not SQLite")
            with self.assertRaises(sqlite3.DatabaseError):
                gate.snapshot_fixture(store, Path(directory) / "snap.db", "v1.0.0")


if __name__ == "__main__":
    unittest.main()
