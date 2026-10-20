#!/usr/bin/env python3
"""Tests for scripts/release_restore_invoke.py.

The tag facts are read from a fixture git repository built on disk, so the
eras are exercised offline against the same `git show` the gate runs. The
end-to-end case drives a fake released binary that verifies the harness's
Ed25519 signature with openssl itself, mints distinct work items per capture,
and enforces the message chain's version flow and store state, so the signed
chain is proven without downloading a release. No network and no installed
store is reachable.
"""

from __future__ import annotations

import base64
import hashlib
import importlib.util
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("release_restore_invoke", Path(__file__).with_name("release_restore_invoke.py"))
assert SPEC and SPEC.loader
invoke = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = invoke
SPEC.loader.exec_module(invoke)

LEGACY_SPEC = importlib.util.spec_from_file_location("release_restore_legacy", Path(__file__).with_name("release_restore_legacy.py"))
assert LEGACY_SPEC and LEGACY_SPEC.loader
legacy = importlib.util.module_from_spec(LEGACY_SPEC)
sys.modules[LEGACY_SPEC.name] = legacy
LEGACY_SPEC.loader.exec_module(legacy)

REPO = Path(__file__).resolve().parents[1]

FORM_A_AUTHORITY = '''func CanonicalAssertion(a SignedAssertion) []byte {
	names := []string{"client_ref", "client_version", "session_ref", "agent_ref", "directory", "worktree", "requested_product_id", "requested_project_ids", "requested_capabilities", "issued_at", "nonce", "surface_range", "envelope_versions", "manifest_digest"}
}
'''
FORM_B_AUTHORITY = '''func CanonicalAssertion(a SignedAssertion) []byte {
	names := []string{"client_ref", "session_ref", "agent_ref", "directory", "worktree", "requested_product_id", "requested_project_ids", "requested_capabilities", "issued_at", "nonce", "manifest_digest"}
}
'''
MAIN_GRANT = 'specs := []string{{{{}}}}\n{{Canonical: "grant", RequiredFields: requiredFields(nestedField("assertion", "signature"))}}\n{{Canonical: "invoke", RequiredFields: requiredFields(nestedField("call_envelope", "{token}", "manifest_digest"))}}\n{{Canonical: "client-register", RequiredFields: requiredFields(field("public_key")){agent}}}\n'
MAIN_NOGRANT = '{{Canonical: "invoke", RequiredFields: requiredFields(nestedField("call_envelope", "manifest_digest"))}}\n{{Canonical: "client-register", RequiredFields: requiredFields(field("public_key")){agent}}}\n'
CONTRACTS = 'const ManifestDigest = "{digest}"\n'
# The work_relate registrations every real source carries, with and without the
# v0.27.0 message operations and their required-input lists.
BASE_CONTRACTS = ('{ID: "concord_work_relate.set_memberships", Tool: "concord_work_relate", Operation: "set_memberships", '
                  'Kind: OperationKind("mutation"), QueryID: "", Capability: Capability("work_relate"), Approval: ApprovalClass("conditional"), Availability: Availability("always")},\n'
                  '{ID: "concord_work_relate.link", Tool: "concord_work_relate", Operation: "link", '
                  'Kind: OperationKind("mutation"), QueryID: "", Capability: Capability("work_relate"), Approval: ApprovalClass("none"), Availability: Availability("always")},\n')
MESSAGE_SEND_ENTRY = ('{ID: "concord_work_relate.message_send", Tool: "concord_work_relate", Operation: "message_send", '
                      'Kind: OperationKind("mutation"), QueryID: "", Capability: Capability("work_relate"), Approval: ApprovalClass("none"), Availability: Availability("always"), InputSchema: "work_relate_message_send_input", ResultSchema: "mutation_result"},\n')
MESSAGE_WITHDRAW_ENTRY = ('{ID: "concord_work_relate.message_withdraw", Tool: "concord_work_relate", Operation: "message_withdraw", '
                          'Kind: OperationKind("mutation"), QueryID: "", Capability: Capability("work_relate"), Approval: ApprovalClass("none"), Availability: Availability("always"), InputSchema: "work_relate_message_withdraw_input", ResultSchema: "mutation_result"},\n')
MESSAGE_INPUTS = ('"work_relate_message_send_input":             {Required: []string{"work_id", "body", "expected_version", "idempotency_key"}, Properties: []string{"work_id", "recipient_work_id", "broadcast", "body", "expected_version", "idempotency_key"}},\n'
                  '"work_relate_message_withdraw_input":         {Required: []string{"work_id", "message_id", "expected_version", "idempotency_key"}, Properties: []string{"work_id", "message_id", "expected_version", "idempotency_key"}},\n')
MESSAGE_CONTRACTS = MESSAGE_SEND_ENTRY + MESSAGE_WITHDRAW_ENTRY + MESSAGE_INPUTS
SEMVER = 'func NegotiateSurfaceVersion(supportedRange string) (string, error) {\n\tsupported := []SemVer{{{Major: 3, Minor: 8, Patch: 0}, {Major: 4, Minor: 0, Patch: 0}}}\n}\n'
LOCATOR_PAIRS = "SELECT product_id,role FROM product_projects"
LOCATOR_TRIPLES = "SELECT product_id,project_id,role FROM product_projects"

# A fake released binary: a Python script that registers the client, verifies
# the assertion signature with openssl when the era has a grant, mints a work
# item per capture, and enforces the message chain's version flow, distinct
# request and idempotency keys, and sent-row state. It pins the era surfaces
# the harness must produce: the canonical field list, the envelope token
# field, and the scope version.
FAKE_BINARY = """#!{python}
import base64, json, os, sqlite3, subprocess, sys, tempfile

request = json.load(sys.stdin)
store = os.environ["CONCORD_DB_PATH"]
verb = sys.argv[1]
if verb == "client-register":
    key = base64.b64decode(request["public_key"])
    assert len(key) == 32, "public key must be the raw 32 bytes"
    assert "work_relate" in request["capabilities"], "work_relate must be offered even before messages exist"
    (open(os.environ["HOME"] + "/registered.pub", "wb")).write(key)
    print(json.dumps({"ok": True}))
    sys.exit(0)
if verb == "grant":
    assert request["max_uses"] == 10, "the grant must keep its max_uses allowance"
    assertion = request["assertion"]
    fields = {json.dumps(invoke.INVOKE_FIELDS)}["names"]
    body = bytearray(b"v1\\x00")
    for name in fields:
        value = assertion.get(name, "")
        if isinstance(value, list):
            value = ",".join(sorted(value))
        raw = str(value).encode()
        body += f"{name}={len(raw)}:".encode() + raw + b"|"
    directory = tempfile.mkdtemp()
    message, signature = directory + "/m", directory + "/s"
    open(message, "wb").write(bytes(body))
    open(signature, "wb").write(base64.b64decode(assertion["signature"]))
    public = directory + "/p"
    raw = open(os.environ["HOME"] + "/registered.pub", "rb").read()
    open(public, "wb").write(bytes.fromhex("302a300506032b6570032100") + raw)
    verify = subprocess.run(["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", public, "-rawin", "-sigfile", signature, "-in", message],
                            capture_output=True)
    assert verify.returncode == 0, "canonical bytes or signature wrong: " + bytes(body).decode("latin1")
    print(json.dumps({"grant_token": "fake-token", "scope_version": "sha256:" + "0" * 64, "surface_version": "4.0.0", "envelope_version": "1.0"}))
    sys.exit(0)
if verb == "invoke":
    envelope = request["call_envelope"]
    assert envelope["manifest_digest"] == {json.dumps(invoke.INVOKE_DIGEST)}, "manifest digest must come from the tag"
    assert {json.dumps(invoke.INVOKE_TOKEN_FIELD)} in envelope, "envelope must carry the era's token field"
    assert envelope["scope_version"].startswith("sha256:"), "scope version must be computed"
    connection = sqlite3.connect(store)
    connection.execute("create table if not exists domain_events (kind text, payload_version int)")
    connection.execute("create table if not exists works (id text primary key, version int)")
    connection.execute("create table if not exists work_messages (message_id text primary key, sender_work_id text, recipient_work_id text, body text, state text)")
    connection.execute("create table if not exists seen (scope text, value text, primary key (scope, value))")
    operation = request["operation"]
    connection.execute("insert into seen values ('request', ?)", (envelope["request_id"],))
    connection.execute("insert into seen values ('idempotency', ?)", (request["input"]["idempotency_key"],))
    if operation == "capture":
        n = connection.execute("select count(*) from works").fetchone()[0] + 1
        work_id = "gate-work-" + str(n)
        connection.execute("insert into works values (?, 2)", (work_id,))
        refs = [{"entity_kind": "work_item", "id": work_id, "version": "2"}]
    elif operation == "message_send":
        inp = request["input"]
        sender = connection.execute("select version from works where id=?", (inp["work_id"],)).fetchone()
        assert sender is not None and sender[0] == inp["expected_version"], "expected_version must be the sender's changed-ref version"
        assert inp["recipient_work_id"] != inp["work_id"], "self-send refuses"
        assert connection.execute("select 1 from works where id=?", (inp["recipient_work_id"],)).fetchone(), "recipient work item must exist"
        version = inp["expected_version"] + 1
        connection.execute("update works set version=? where id=?", (version, inp["work_id"]))
        message_id = "msg:" + format(inp["expected_version"], "032x")
        connection.execute("insert into work_messages values (?, ?, ?, ?, 'sent')",
                           (message_id, inp["work_id"], inp["recipient_work_id"], inp["body"]))
        refs = [{"entity_kind": "work_item", "id": inp["work_id"], "version": str(version)}]
    elif operation == "message_withdraw":
        inp = request["input"]
        state = connection.execute("select state from work_messages where message_id=?", (inp["message_id"],)).fetchone()
        assert state == ("sent",), "withdrawal needs the exact sent message"
        sender = connection.execute("select version from works where id=?", (inp["work_id"],)).fetchone()
        assert sender is not None and sender[0] == inp["expected_version"], "expected_version must be the send's changed-ref version"
        version = inp["expected_version"] + 1
        connection.execute("update works set version=? where id=?", (version, inp["work_id"]))
        connection.execute("update work_messages set state='withdrawn' where message_id=?", (inp["message_id"],))
        refs = [{"entity_kind": "work_item", "id": inp["work_id"], "version": str(version)}]
    else:
        connection.close()
        print(json.dumps({"outcome": "error", "error": {"message": "unknown operation " + operation}}))
        sys.exit(1)
    kinds = {"capture": ("work.created", 2), "message_send": ("work.message_sent", 1), "message_withdraw": ("work.message_withdrawn", 1)}
    connection.execute("insert into domain_events values (?, ?)", kinds[operation])
    connection.commit()
    connection.close()
    print(json.dumps({"outcome": "ok", "changed_refs": refs}))
    sys.exit(0)
print(json.dumps({"outcome": "error", "error": {"message": verb + " unimplemented"}}))
sys.exit(1)
"""


def commit(repo: Path, paths: dict[str, str], tag: str) -> None:
    for name, content in paths.items():
        (repo / name).parent.mkdir(parents=True, exist_ok=True)
        (repo / name).write_text(content, encoding="utf-8")
    subprocess.run(["git", "-C", str(repo), "add", "-A"], check=True, capture_output=True)
    subprocess.run(["git", "-C", str(repo), "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", tag],
                   check=True, capture_output=True, env={**os.environ, "GIT_CONFIG_NOSYSTEM": "1"})
    subprocess.run(["git", "-C", str(repo), "tag", tag], check=True, capture_output=True)


def fixture_repository(root: Path) -> Path:
    repo = root / "repo"
    subprocess.run(["git", "init", "-q", "-b", "main", str(repo)], check=True, capture_output=True)
    digest = "sha256:" + "a" * 64

    def contracts(digest: str, messages: bool) -> str:
        return BASE_CONTRACTS + (MESSAGE_CONTRACTS if messages else "") + CONTRACTS.format(digest=digest)

    commit(repo, {"cmd/concord/main.go": MAIN_GRANT.format(token="grant_ref", agent=""),
                  "internal/agent/authority.go": FORM_A_AUTHORITY,
                  "internal/agent/semver.go": SEMVER,
                  "internal/agent/generated_contracts.go": contracts(digest, messages=False),
                  "internal/store/project_locator.go": LOCATOR_PAIRS}, "v0.26.1")
    commit(repo, {"cmd/concord/main.go": MAIN_GRANT.format(token="grant_ref", agent=""),
                  "internal/agent/authority.go": FORM_A_AUTHORITY,
                  "internal/agent/generated_contracts.go": contracts("sha256:" + "a" * 64, messages=True)}, "v0.37.2")
    commit(repo, {"cmd/concord/main.go": MAIN_GRANT.format(token="grant_token", agent=""),
                  "internal/agent/authority.go": FORM_B_AUTHORITY,
                  "internal/agent/generated_contracts.go": contracts("sha256:" + "b" * 64, messages=True)}, "v0.40.0")
    commit(repo, {"cmd/concord/main.go": MAIN_NOGRANT.format(agent=', Optional: "agent_scope"'),
                  "internal/agent/authority.go": "",
                  "internal/store/project_locator.go": LOCATOR_TRIPLES,
                  "internal/agent/generated_contracts.go": contracts("sha256:" + "c" * 64, messages=True)}, "v4.6.0")
    return repo


class InvokeUnitTest(unittest.TestCase):
    def test_rfc3339nano_drops_trailing_zeros_like_go(self) -> None:
        self.assertEqual(invoke.rfc3339nano(datetime(2026, 10, 19, 12, 0, 0, 123450, tzinfo=timezone.utc)), "2026-10-19T12:00:00.12345Z")
        self.assertEqual(invoke.rfc3339nano(datetime(2026, 10, 19, 12, 0, 0, 120000, tzinfo=timezone.utc)), "2026-10-19T12:00:00.12Z")
        self.assertEqual(invoke.rfc3339nano(datetime(2026, 10, 19, 12, 0, 0, 0, tzinfo=timezone.utc)), "2026-10-19T12:00:00Z")

    def test_canonical_assertion_bytes(self) -> None:
        names = ["client_ref", "requested_capabilities", "issued_at", "missing_here"]
        fields = {"client_ref": "gäte", "requested_capabilities": "a,b", "issued_at": "2026-10-19T12:00:00Z"}
        self.assertEqual(invoke.canonical_assertion(names, fields),
                         b"v1\x00client_ref=5:g\xc3\xa4te|requested_capabilities=3:a,b|issued_at=20:2026-10-19T12:00:00Z|missing_here=0:|")

    def test_scope_version_both_formulas(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            store = Path(name) / "s.db"
            connection = sqlite3.connect(store)
            connection.execute("CREATE TABLE product_projects (product_id TEXT, project_id TEXT, role TEXT)")
            connection.execute("INSERT INTO product_projects VALUES ('gate-product', 'gate-project', 'primary')")
            connection.commit()
            connection.close()
            import hashlib
            pairs = hashlib.sha256(b"gate-product|primary").hexdigest()
            triples = hashlib.sha256(b'[["gate-product","gate-project","primary"]]').hexdigest()
            self.assertEqual(invoke.scope_version("pairs", store), "sha256:" + pairs)
            self.assertEqual(invoke.scope_version("triples", store), "sha256:" + triples)

    def test_tag_facts_read_the_eras_from_the_repository(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            oldest = invoke.tag_facts(repo, "v0.26.1")
            self.assertTrue(oldest["has_grant"])
            self.assertFalse(oldest["has_messages"])
            early = invoke.tag_facts(repo, "v0.37.2")
            self.assertTrue(early["has_grant"])
            self.assertTrue(early["has_messages"])
            self.assertIn("client_version", early["names"])
            self.assertEqual(early["surface"], "4.0.0")
            self.assertEqual(early["manifest_digest"], "sha256:" + "a" * 64)
            self.assertFalse(early["agent_scope"])
            self.assertEqual(early["scope_formula"], "pairs")
            middle = invoke.tag_facts(repo, "v0.40.0")
            self.assertTrue(middle["has_grant"])
            self.assertTrue(middle["has_messages"])
            self.assertNotIn("client_version", middle["names"])
            self.assertNotIn("surface", middle)
            self.assertEqual(middle["manifest_digest"], "sha256:" + "b" * 64)
            modern = invoke.tag_facts(repo, "v4.6.0")
            self.assertFalse(modern["has_grant"])
            self.assertTrue(modern["has_messages"])
            self.assertTrue(modern["agent_scope"])
            self.assertEqual(modern["scope_formula"], "triples")
            self.assertEqual(modern["manifest_digest"], "sha256:" + "c" * 64)

    def mutate_contracts(self, repo: Path, base: str, tag: str, replacement: str, search: str | None = None) -> None:
        """Tag a variant of the v0.37.2 source whose contracts carry `replacement` instead of `search`."""
        subprocess.run(["git", "-C", str(repo), "checkout", "-q", "-B", "mutant", "v0.37.2"], check=True, capture_output=True)
        text = (repo / "internal/agent/generated_contracts.go").read_text(encoding="utf-8")
        (repo / "internal/agent/generated_contracts.go").write_text(text.replace(search or MESSAGE_CONTRACTS, replacement), encoding="utf-8")
        subprocess.run(["git", "-C", str(repo), "add", "-A"], check=True, capture_output=True)
        subprocess.run(["git", "-C", str(repo), "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", tag],
                       check=True, capture_output=True, env={**os.environ, "GIT_CONFIG_NOSYSTEM": "1"})
        subprocess.run(["git", "-C", str(repo), "tag", tag], check=True, capture_output=True)

    def assert_tag_facts_hold(self, repo: Path, tag: str) -> None:
        with self.assertRaises(invoke.InvokeError) as raised:
            invoke.tag_facts(repo, tag)
        self.assertEqual(raised.exception.stage, "tag-facts")
        return raised.exception

    def test_a_partially_registered_message_era_holds(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            self.mutate_contracts(repo, "v0.37.2", "v0.37.3-partial", MESSAGE_CONTRACTS.splitlines()[0] + "\n")
            error = self.assert_tag_facts_hold(repo, "v0.37.3-partial")
            self.assertIn("message_withdraw", error.detail)

    def test_an_approval_bearing_message_operation_holds(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            self.mutate_contracts(repo, "v0.37.2", "v0.37.3-approval", MESSAGE_CONTRACTS.replace('Approval: ApprovalClass("none"), Availability: Availability("always"), InputSchema: "work_relate_message_send_input"',
                                                                                                 'Approval: ApprovalClass("required"), Availability: Availability("always"), InputSchema: "work_relate_message_send_input"'))
            error = self.assert_tag_facts_hold(repo, "v0.37.3-approval")
            self.assertIn("approval", error.detail)
            self.assertIn("message_send", error.detail)

    def test_a_message_operation_with_a_moved_input_list_holds(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            self.mutate_contracts(repo, "v0.37.2", "v0.37.3-inputs", MESSAGE_CONTRACTS.replace('"work_relate_message_withdraw_input":         {Required: []string{"work_id", "message_id", "expected_version", "idempotency_key"}', '"work_relate_message_withdraw_input":         {'))
            error = self.assert_tag_facts_hold(repo, "v0.37.3-inputs")
            self.assertIn("work_relate_message_withdraw_input", error.detail)

    def test_a_changed_required_message_input_holds(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            self.mutate_contracts(repo, "v0.37.2", "v0.37.3-changed", MESSAGE_CONTRACTS.replace(
                '"work_relate_message_send_input":             {Required: []string{"work_id", "body", "expected_version", "idempotency_key"}',
                '"work_relate_message_send_input":             {Required: []string{"work_id", "recipient_work_id", "body", "expected_version", "idempotency_key"}'))
            error = self.assert_tag_facts_hold(repo, "v0.37.3-changed")
            self.assertIn("message_send", error.detail)
            self.assertIn("recipient_work_id", error.detail)

    def test_a_moved_input_schema_binding_holds(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            self.mutate_contracts(repo, "v0.37.2", "v0.37.3-schema", MESSAGE_CONTRACTS.replace('InputSchema: "work_relate_message_send_input"', 'InputSchema: "work_relate_message_send_v2_input"'))
            error = self.assert_tag_facts_hold(repo, "v0.37.3-schema")
            self.assertIn("work_relate_message_send_v2_input", error.detail)

    def test_a_capability_or_kind_change_holds(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            self.mutate_contracts(repo, "v0.37.2", "v0.37.3-capability", MESSAGE_CONTRACTS.replace('Capability: Capability("work_relate"), Approval: ApprovalClass("none"), Availability: Availability("always"), InputSchema: "work_relate_message_withdraw_input"',
                                                                                                   'Capability: Capability("cross_scope"), Approval: ApprovalClass("none"), Availability: Availability("always"), InputSchema: "work_relate_message_withdraw_input"'))
            self.assert_tag_facts_hold(repo, "v0.37.3-capability")

    def test_a_source_without_readable_work_relate_registrations_holds(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            self.mutate_contracts(repo, "v0.37.2", "v0.37.3-unknown", "", search=BASE_CONTRACTS + MESSAGE_CONTRACTS)
            self.assert_tag_facts_hold(repo, "v0.37.3-unknown")

    def test_message_rows_the_reader_cannot_parse_hold_instead_of_downgrading(self) -> None:
        """Spacing-mutated message registrations next to readable relate rows are unknown support, not capture-only."""
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            self.mutate_contracts(repo, "v0.37.2", "v0.37.3-spacing",
                                  MESSAGE_CONTRACTS.replace('{ID: "concord_work_relate.message_send"', '{ID:  "concord_work_relate.message_send"')
                                  .replace('{ID: "concord_work_relate.message_withdraw"', '{ID:  "concord_work_relate.message_withdraw"'))
            error = self.assert_tag_facts_hold(repo, "v0.37.3-spacing")
            self.assertIn("outside the registration shape", error.detail)

    def test_message_traces_without_registrations_hold_instead_of_downgrading(self) -> None:
        """Schema references for the message ops with no readable registration rows refuse too."""
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            self.mutate_contracts(repo, "v0.37.2", "v0.37.3-traces", MESSAGE_INPUTS)
            error = self.assert_tag_facts_hold(repo, "v0.37.3-traces")
            self.assertIn("message_send", error.detail)

    def test_the_plan_lists_only_the_steps_the_source_prescribes(self) -> None:
        capture_only = invoke.invoke_plan({"has_grant": True, "has_messages": False})
        grant_messages = invoke.invoke_plan({"has_grant": True, "has_messages": True})
        bare_messages = invoke.invoke_plan({"has_grant": False, "has_messages": True})
        bare_capture = invoke.invoke_plan({"has_grant": False, "has_messages": False})
        self.assertEqual(capture_only, ("client-register", "grant", "invoke:concord_work_define.capture"))
        self.assertEqual(grant_messages, ("client-register", "grant", "invoke:concord_work_define.capture", "invoke:concord_work_define.capture",
                                          "invoke:concord_work_relate.message_send", "invoke:concord_work_relate.message_withdraw"))
        self.assertEqual(bare_messages, ("client-register", "invoke:concord_work_define.capture", "invoke:concord_work_define.capture",
                                         "invoke:concord_work_relate.message_send", "invoke:concord_work_relate.message_withdraw"))
        self.assertEqual(bare_capture, ("client-register", "invoke:concord_work_define.capture"))
        self.assertEqual(len({capture_only, grant_messages, bare_messages, bare_capture}), 4)
        self.assertNotIn("grant", bare_messages)

    def test_a_grant_era_without_a_field_list_refuses_by_name(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            subprocess.run(["git", "-C", str(repo), "checkout", "-q", "-b", "broken", "v0.37.2"], check=True, capture_output=True)
            (repo / "internal/agent/authority.go").write_text("nothing here", encoding="utf-8")
            subprocess.run(["git", "-C", str(repo), "add", "-A"], check=True, capture_output=True)
            subprocess.run(["git", "-C", str(repo), "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "x"],
                           check=True, capture_output=True, env={**os.environ, "GIT_CONFIG_NOSYSTEM": "1"})
            subprocess.run(["git", "-C", str(repo), "tag", "v0.38.0"], check=True, capture_output=True)
            with self.assertRaises(invoke.InvokeError) as raised:
                invoke.tag_facts(repo, "v0.38.0")
            self.assertEqual(raised.exception.stage, "tag-facts")


class InvokeChainTest(unittest.TestCase):
    """The whole chain against one fake binary per era, with real openssl signatures."""

    def run_phase(self, tag: str) -> None:
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            repo = fixture_repository(root)
            case = root / "case"
            (case / "home").mkdir(parents=True)
            (case / "store").mkdir(parents=True)
            facts = invoke.tag_facts(repo, tag)
            script = root / "binary"
            script.write_text(FAKE_BINARY.format(python=sys.executable,
                                                 **{"": ""},
                                                 ) if False else FAKE_BINARY.replace("#!{python}", f"#!{sys.executable}")
                              .replace('{json.dumps(invoke.INVOKE_FIELDS)}["names"]', json.dumps({"names": facts["names"]}["names"] if facts["names"] else []))
                              .replace('{json.dumps(invoke.INVOKE_DIGEST)}', json.dumps(facts["manifest_digest"]))
                              .replace('{json.dumps(invoke.INVOKE_TOKEN_FIELD)}', json.dumps("grant_token" if facts["has_grant"] and "client_version" not in facts["names"] else "grant_ref" if facts["has_grant"] else "scope_version")), encoding="utf-8")
            script.chmod(0o755)
            env = {"PATH": "/usr/bin:/bin", "HOME": str(case / "home"), "TMPDIR": str(case / "tmp"), "CONCORD_DB_PATH": str(case / "store" / "concord.db"),
                   "GIT_CONFIG_NOSYSTEM": "1"}
            Path(env["TMPDIR"]).mkdir(parents=True, exist_ok=True)
            connection = sqlite3.connect(env["CONCORD_DB_PATH"])
            connection.execute("CREATE TABLE product_projects (product_id TEXT, project_id TEXT, role TEXT)")
            connection.execute("INSERT INTO product_projects VALUES ('gate-product', 'gate-project', 'primary')")
            connection.commit()
            connection.close()

            def run(verb: str, request: dict) -> tuple[int, str, str]:
                proc = subprocess.run([str(script), verb], env=env, input=json.dumps(request), capture_output=True, text=True, timeout=120)
                return proc.returncode, proc.stdout, proc.stderr

            record = invoke.run_invoke_phase(run, repo, tag, case, Path(env["CONCORD_DB_PATH"]))
            self.assertEqual(record["outcome"], "ok")
            self.assertEqual(record["plan"], list(invoke.invoke_plan(facts)))
            expected = [("work.created", 2)] * (1 if not facts["has_messages"] else 2)
            if facts["has_messages"]:
                expected += [("work.message_sent", 1), ("work.message_withdrawn", 1)]
            connection = sqlite3.connect(env["CONCORD_DB_PATH"])
            self.assertEqual(connection.execute("SELECT kind, payload_version FROM domain_events ORDER BY rowid").fetchall(), expected)
            if facts["has_messages"]:
                # The sender's version advanced only through its own changed-ref chain:
                # capture 2 -> send 3 -> withdraw 4, while the recipient stays at 2.
                self.assertEqual(connection.execute("SELECT id, version FROM works ORDER BY id").fetchall(),
                                 [("gate-work-1", 4), ("gate-work-2", 2)])
                self.assertEqual(connection.execute("SELECT sender_work_id, recipient_work_id, state FROM work_messages").fetchall(),
                                 [("gate-work-1", "gate-work-2", "withdrawn")])
            connection.close()

    def test_the_signed_chain_each_era(self) -> None:
        for tag in ("v0.26.1", "v0.37.2", "v0.40.0", "v4.6.0"):
            with self.subTest(tag=tag):
                self.run_phase(tag)

    def test_a_refusing_grant_names_its_stage(self) -> None:
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            repo = fixture_repository(root)
            case = root / "case"
            case.mkdir(parents=True)
            env = {"PATH": "/usr/bin:/bin", "HOME": str(case / "home"), "TMPDIR": str(case / "tmp"), "CONCORD_DB_PATH": str(case / "store" / "concord.db")}
            for value in (env["HOME"], env["TMPDIR"], str(case / "store")):
                Path(value).mkdir(parents=True, exist_ok=True)

            def run(verb: str, request: dict) -> tuple[int, str, str]:
                if verb == "client-register":
                    return 0, "{}", ""
                return 3, "", "the grant refused the harness assertion"

            with self.assertRaises(invoke.InvokeError) as raised:
                invoke.run_invoke_phase(run, repo, "v0.37.2", case, Path(env["CONCORD_DB_PATH"]))
            self.assertEqual(raised.exception.stage, "grant")
            self.assertIn("refused the harness assertion", raised.exception.detail)


def ref(work_id: str, version: int) -> dict:
    """A changed ref in the wire shape every era emits: the version is a decimal string (envelope.go)."""
    return {"entity_kind": "work_item", "id": work_id, "version": str(version)}


def ok_capture(work_id: str = "gate-work-1", version: int = 2) -> str:
    return json.dumps({"outcome": "ok", "changed_refs": [ref(work_id, version)]})


class WorkRefTest(unittest.TestCase):
    """The changed-ref reader accepts exactly the wire shape: decimal strings, never invented types."""

    def test_a_wire_shaped_ref_reads_back(self) -> None:
        self.assertEqual(invoke.work_ref({"changed_refs": [ref("gate-work-1", 2)]}, "invoke"), ("gate-work-1", 2))
        self.assertEqual(invoke.work_ref({"changed_refs": [ref("gate-work-1", 12)]}, "invoke"), ("gate-work-1", 12))

    def test_non_string_versions_are_malformed(self) -> None:
        for bad in (2, True, None, 2.0, ["2"]):
            with self.assertRaises(invoke.InvokeError) as raised:
                invoke.work_ref({"changed_refs": [{"entity_kind": "work_item", "id": "a", "version": bad}]}, "invoke")
            self.assertEqual(raised.exception.stage, "invoke")
            self.assertIn("version", raised.exception.detail)

    def test_non_canonical_decimal_strings_are_malformed(self) -> None:
        for bad in ("0", "02", "+2", " 2", "2 ", "2.0", "two", ""):
            with self.assertRaises(invoke.InvokeError) as raised:
                invoke.work_ref({"changed_refs": [{"entity_kind": "work_item", "id": "a", "version": bad}]}, "invoke")
            self.assertIn("version", raised.exception.detail)


class StubChain:
    """A `run` stub: registration succeeds, each invoke returns the next scripted response."""

    def __init__(self, responses: list[tuple[int, str, str]]) -> None:
        self.responses = list(responses)
        self.invocations: list[dict] = []

    def run(self, verb: str, request: dict) -> tuple[int, str, str]:
        if verb == "client-register":
            return 0, "{}", ""
        if verb == "grant":
            return 0, json.dumps({"grant_token": "stub-token", "scope_version": "sha256:" + "0" * 64}), ""
        self.invocations.append(request)
        return self.responses.pop(0)


class MessageRefusalTest(unittest.TestCase):
    """A message chain that cannot prove its own inputs holds by name; it never downgrades to capture-only."""

    SENT = ("msg:" + "0" * 31 + "2", "gate-work-1", "gate-work-2", "gate release-restore message chain", "sent")

    def sent_store(self, rows: list[tuple[str, str, str, str]]) -> Path:
        store = Path(tempfile.mkdtemp()) / "store.db"
        connection = sqlite3.connect(store)
        connection.execute("CREATE TABLE work_messages (message_id TEXT, sender_work_id TEXT, recipient_work_id TEXT, body TEXT, state TEXT)")
        connection.executemany("INSERT INTO work_messages VALUES (?, ?, ?, ?, ?)", rows)
        connection.commit()
        connection.close()
        return store

    def phase(self, responses: list[tuple[int, str, str]], store: Path | None = None) -> invoke.InvokeError:
        """Run a message-era phase whose store by default shows the one well-formed sent row."""
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            case = Path(name) / "case"
            case.mkdir()
            with self.assertRaises(invoke.InvokeError) as raised:
                invoke.run_invoke_phase(StubChain(responses).run, repo, "v0.37.2", case,
                                        store or self.sent_store([self.SENT]))
            return raised.exception

    def test_an_ambiguous_first_capture_refuses_by_name(self) -> None:
        error = self.phase([(0, json.dumps({"outcome": "ok", "changed_refs": [ref("a", 2), ref("b", 2)]}), "")])
        self.assertEqual(error.stage, "invoke")
        self.assertIn("exactly one work_item", error.detail)

    def test_a_non_integer_version_refuses(self) -> None:
        error = self.phase([(0, json.dumps({"outcome": "ok", "changed_refs": [{"entity_kind": "work_item", "id": "a", "version": 2}]}), "")])
        self.assertEqual((error.stage, "version" in error.detail), ("invoke", True))

    def test_missing_changed_refs_refuse(self) -> None:
        error = self.phase([(0, json.dumps({"outcome": "ok", "changed_refs": []}), "")])
        self.assertEqual(error.stage, "invoke")

    def test_a_second_capture_returning_the_sender_refuses(self) -> None:
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture(), "")])
        self.assertEqual(error.stage, "capture-2")
        self.assertIn("sender", error.detail)

    def test_a_send_changed_ref_for_another_work_holds(self) -> None:
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""),
                            (0, json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-9", 3)]}), "")])
        self.assertEqual(error.stage, "message-send")
        self.assertIn("not the sender", error.detail)

    def test_a_withdraw_changed_ref_for_another_work_holds(self) -> None:
        sent = json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-1", 3)]})
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""), (0, sent, ""),
                            (0, json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-9", 4)]}), "")])
        self.assertEqual(error.stage, "message-withdraw")
        self.assertIn("not the sender", error.detail)

    def test_a_withdraw_without_a_changed_ref_holds(self) -> None:
        sent = json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-1", 3)]})
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""), (0, sent, ""),
                            (0, json.dumps({"outcome": "ok", "changed_refs": []}), "")])
        self.assertEqual(error.stage, "message-withdraw")

    def test_a_failing_send_names_the_operation(self) -> None:
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""),
                            (0, json.dumps({"outcome": "error", "error": {"message": "recipient unknown"}}), "")])
        self.assertEqual(error.stage, "message-send")
        self.assertIn("recipient unknown", error.detail)

    def test_a_nonzero_send_invoke_holds(self) -> None:
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""), (3, "", "writer crashed")])
        self.assertEqual(error.stage, "message-send")
        self.assertIn("writer crashed", error.detail)

    def test_a_nonzero_withdraw_invoke_holds(self) -> None:
        sent = json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-1", 3)]})
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""), (0, sent, ""), (4, "", "")])
        self.assertEqual(error.stage, "message-withdraw")

    def test_a_send_that_reports_no_sender_version_holds(self) -> None:
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""),
                            (0, json.dumps({"outcome": "ok", "changed_refs": []}), "")])
        self.assertEqual(error.stage, "message-send")

    def test_an_ambiguous_sent_row_refuses_by_name(self) -> None:
        store = self.sent_store([self.SENT, ("msg:" + "b" * 32, "gate-work-1", "gate-work-2", "other body", "sent")])
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""),
                            (0, json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-1", 3)]}), "")], store)
        self.assertEqual(error.stage, "message-id")

    def test_a_missing_sent_row_refuses_by_name(self) -> None:
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""),
                            (0, json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-1", 3)]}), "")],
                           self.sent_store([]))
        self.assertEqual(error.stage, "message-id")
        self.assertIn("exactly one", error.detail)

    def test_a_malformed_message_id_refuses_by_name(self) -> None:
        error = self.phase([(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""),
                            (0, json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-1", 3)]}), "")],
                           self.sent_store([("not-a-message-id", "gate-work-1", "gate-work-2", "other body", "sent")]))
        self.assertEqual(error.stage, "message-id")

    def test_the_message_chain_sends_the_stable_intersection_only(self) -> None:
        """The send input carries exactly the stable direct-send fields and never an approval field."""
        responses = [(0, ok_capture(), ""), (0, ok_capture("gate-work-2"), ""),
                     (0, json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-1", 3)]}), ""),
                     (0, json.dumps({"outcome": "ok", "changed_refs": [ref("gate-work-1", 4)]}), "")]
        stub = StubChain(responses)
        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            case = Path(name) / "case"
            case.mkdir()
            record = invoke.run_invoke_phase(stub.run, repo, "v0.37.2", case, self.sent_store([self.SENT]))
            self.assertEqual(record["outcome"], "ok")
        send = stub.invocations[2]
        self.assertEqual(send["tool"], "concord_work_relate")
        self.assertEqual(send["operation"], "message_send")
        self.assertEqual(sorted(send["input"]), ["body", "expected_version", "idempotency_key", "recipient_work_id", "work_id"])
        self.assertNotIn("approval", json.dumps(send))
        withdraw = stub.invocations[3]
        self.assertEqual(withdraw["operation"], "message_withdraw")
        self.assertEqual(sorted(withdraw["input"]), ["expected_version", "idempotency_key", "message_id", "work_id"])
        self.assertTrue(withdraw["input"]["message_id"].startswith("msg:"))
        request_ids = [call["call_envelope"]["request_id"] for call in stub.invocations]
        idempotency = [call["input"]["idempotency_key"] for call in stub.invocations]
        self.assertEqual(len(set(request_ids)), 4)
        self.assertEqual(len(set(idempotency)), 4)


# Synthetic sources shaped like the v0.1.0-era writers the legacy proof reasons
# about: direct-marshaling mutationPayload, string ChangedRef.Version, the
# capture->mutation_result schema binding, the integer version definition, the
# operator bypass, and the bootstrap store writers. The corrected variant
# mirrors v0.8.0: only mutations.go changes to integer result refs.
LEGACY_SOURCES = {
    "cmd/concord/main.go": (
        "package main\n"
        "\n"
        "func runJSONCommand(command string, raw []byte, s *store.Store, service *agent.Service, out io.Writer) int {\n"
        "\tswitch command {\n"
        "\tcase \"invoke\":\n"
        "\t\treturn runInvoke(raw, s, service, out)\n"
        "\tcase \"grant\":\n"
        "\t\treturn runGrant(raw, service, out)\n"
        "\tdefault:\n"
        "\t\treturn runInternal(command, raw, service, s, out)\n"
        "\t}\n"
        "}\n"
        "\n"
        "func writeOperatorResult(command string, s *store.Store, refs []operatorRef, out io.Writer) int {\n"
        "\t// operatorResponse versions come from strconv.FormatInt, never agent.Envelope.\n"
        "\tresponse := operatorResponse{OK: true, ChangedRefs: changed}\n"
        "\treturn writeJSON(out, response)\n"
        "}\n"
    ),
    "internal/agent/envelope.go": (
        "package agent\n"
        "\n"
        "type ChangedRef struct {\n"
        "\tEntityKind string `json:\"entity_kind\"`\n"
        "\tID         string `json:\"id\"`\n"
        "\tVersion    string `json:\"version\"`\n"
        "}\n"
        "\n"
        "func (e Envelope) MarshalJSON() ([]byte, error) {\n"
        "\tif err := e.Validate(); err != nil {\n"
        "\t\treturn nil, err\n"
        "\t}\n"
        "\tb, err := json.Marshal(wire(e))\n"
        "\treturn b, err\n"
        "}\n"
    ),
    "internal/agent/mutations.go": (
        "package agent\n"
        "\n"
        "func (r runtime) captureEffect(workID string) mutationEffect {\n"
        "\treturn func(ctx context.Context, tx *sql.Tx, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {\n"
        "\t\tresult, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{Kind: \"work.created\", PayloadVersion: 2}}})\n"
        "\t\tif err != nil {\n"
        "\t\t\treturn nil, nil, nil, err\n"
        "\t\t}\n"
        "\t\treturn mutationPayload([]ChangedRef{{EntityKind: \"work_item\", ID: workID, Version: \"2\"}}, intents), result.EventIDs, []ChangedRef{{EntityKind: \"work_item\", ID: workID, Version: \"2\"}}, nil\n"
        "\t}\n"
        "}\n"
        "\n"
        "func mutationPayload(changed []ChangedRef, intents []NextIntent) json.RawMessage {\n"
        "\tb, _ := json.Marshal(map[string]any{\"changed_refs\": changed, \"next_valid_intents\": intents})\n"
        "\treturn b\n"
        "}\n"
    ),
    "internal/agent/payload_validation.go": (
        "package agent\n"
        "\n"
        "var operationPayloadSchemas = map[string]string{\"concord_work_define.capture\": \"mutation_result\"}\n"
        "\n"
        "func ValidateOperationPayload(tool, operation string, data []byte, result bool) error {\n"
        "\tname := operationPayloadSchemas[tool+\".\"+operation]\n"
        "\tif err := ValidateGeneratedPayload(name, data); err != nil {\n"
        "\t\treturn err\n"
        "\t}\n"
        "\treturn ValidatePayloadSchema(name, data)\n"
        "}\n"
        "\n"
        "func validateSchemaValue(value any, schema map[string]any, root map[string]any, path string) error {\n"
        "\tif types, ok := schema[\"type\"]; ok && !matchesAnyType(value, types) {\n"
        "\t\treturn fmt.Errorf(\"type mismatch at %s\", path)\n"
        "\t}\n"
        "\treturn nil\n"
        "}\n"
    ),
    "internal/agent/generated_payload_schemas.go": (
        "package agent\n"
        "\n"
        "const GeneratedPayloadSchemaDocument = `{\"$defs\":{\"mutation_result\":{\"properties\":{\"changed_refs\":{\"items\":"
        "{\"properties\":{\"version\":{\"$ref\":\"#/$defs/version\"}}}}}},\"version\":{\"minimum\":1,\"type\":\"integer\"}}}`\n"
    ),
    "internal/agent/generated_contracts.go": (
        "package agent\n"
        "\n"
        "var GeneratedPayloadRules = map[string]PayloadRule{\"mutation_result\": {Required: []string{\"changed_refs\", \"next_valid_intents\"}}}\n"
        "\n"
        "func ValidateGeneratedPayload(schemaName string, data []byte) error {\n"
        "\trule, ok := GeneratedPayloadRules[schemaName]\n"
        "\tif !ok {\n"
        "\t\treturn fmt.Errorf(\"unknown payload schema %s\", schemaName)\n"
        "\t}\n"
        "\tvar object map[string]json.RawMessage\n"
        "\tif err := json.Unmarshal(data, &object); err != nil {\n"
        "\t\treturn err\n"
        "\t}\n"
        "\tfor _, name := range rule.Required {\n"
        "\t\tif _, ok := object[name]; !ok {\n"
        "\t\t\treturn fmt.Errorf(\"missing payload field %s\", name)\n"
        "\t\t}\n"
        "\t}\n"
        "\treturn nil\n"
        "}\n"
    ),
    "internal/store/operator.go": (
        "package store\n"
        "\n"
        "func (s *Store) CreateProductWithProject(ctx context.Context, request ProductCreation) (ApplyOperationResult, error) {\n"
        "\treturn ApplyOperationWithResult(ctx, s, Operation{Events: []Event{\n"
        "\t\t{Kind: \"product.created\", PayloadVersion: 1}, {Kind: \"project.created\", PayloadVersion: 1}, {Kind: \"product_project.added\", PayloadVersion: 1},\n"
        "\t}})\n"
        "}\n"
    ),
    "internal/store/project_locator.go": (
        "package store\n"
        "\n"
        "func (s *Store) AddProjectLocator(ctx context.Context, projectID string, locator ProjectLocator, expectedVersion int64) error {\n"
        "\treturn s.applyLocatorEvent(ctx, projectID, \"project.locator_added\", locator, expectedVersion)\n"
        "}\n"
    ),
}
CORRECTED_MUTATIONS = LEGACY_SOURCES["internal/agent/mutations.go"].replace(
    "mutationPayload([]ChangedRef{{EntityKind: \"work_item\", ID: workID, Version: \"2\"}}, intents)",
    "mutationPayload([]ResultRef{{EntityKind: \"work_item\", ID: workID, Version: 2}}, intents)",
).replace(
    "func mutationPayload(changed []ChangedRef, intents []NextIntent) json.RawMessage {",
    "// v0.8.0 correction: integer result refs converted from the stored string version.\nfunc mutationPayload(changed []ResultRef, intents []NextIntent) json.RawMessage {",
)


def git_bytes(repo: Path, *argv: str) -> bytes:
    proc = subprocess.run(["git", "-C", str(repo), *argv], capture_output=True, timeout=120)
    if proc.returncode != 0:
        raise AssertionError(f"git {' '.join(argv)} exited {proc.returncode}: {proc.stderr.decode()[-200:]}")
    return proc.stdout


def blob_sha256(repo: Path, rev: str, path: str) -> str:
    return hashlib.sha256(git_bytes(repo, "show", f"{rev}:{path}")).hexdigest()


class LegacyProofTest(unittest.TestCase):
    """The whole-file source proof that alone admits the bootstrap-only variant.

    Fixture tables are built from real `git show` blob hashes of the fixture's
    own objects, never invented; the committed table is verified against this
    checkout's actual v0.1.0-v0.8.0 tags with the same independent hashing.
    """

    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.repo = self.tempdir.name and Path(self.tempdir.name) / "repo"
        subprocess.run(["git", "init", "-q", "-b", "main", str(self.repo)], check=True, capture_output=True)
        self.commits = {}
        self.commits["legacy-a"] = self.commit_tagged(LEGACY_SOURCES, "legacy-a")
        sources = dict(LEGACY_SOURCES)
        sources["internal/agent/mutations.go"] = CORRECTED_MUTATIONS
        self.commits["fixed-a"] = self.commit_tagged(sources, "fixed-a")
        self.rows = (
            {"tag": "legacy-a", "commit": self.commits["legacy-a"], "variant": legacy.LEGACY_VARIANT,
             "owners": {path: blob_sha256(self.repo, self.commits["legacy-a"], path) for path in legacy.LEGACY_OWNERS}},
            {"tag": "fixed-a", "commit": self.commits["fixed-a"], "variant": legacy.CORRECTED_VARIANT,
             "owners": {path: blob_sha256(self.repo, self.commits["fixed-a"], path) for path in legacy.LEGACY_OWNERS}},
        )
        self.addCleanup(self.tempdir.cleanup)

    def pinned(self, rows: tuple | None = None):
        """Serve a fixture pin table to the module for the duration of one test."""
        return patch.object(legacy, "LEGACY_SOURCE_PINS", rows or self.rows)

    def commit_tagged(self, paths: dict[str, str], tag: str) -> str:
        for name, content in paths.items():
            (self.repo / name).parent.mkdir(parents=True, exist_ok=True)
            (self.repo / name).write_text(content, encoding="utf-8")
        env = {**os.environ, "GIT_CONFIG_NOSYSTEM": "1"}
        subprocess.run(["git", "-C", str(self.repo), "add", "-A"], check=True, capture_output=True)
        subprocess.run(["git", "-C", str(self.repo), "-c", "user.name=t", "-c", "user.email=t@example.invalid",
                        "commit", "--allow-empty", "-qm", tag], check=True, capture_output=True, env=env)
        subprocess.run(["git", "-C", str(self.repo), "tag", tag], check=True, capture_output=True)
        return git_bytes(self.repo, "rev-parse", f"{tag}^{{commit}}").decode().strip()

    def select(self, tag: str, remote: str | None):
        return legacy.select_variant(self.repo, tag, remote)

    def shadow_blob(self, rev: str, path: str, content: bytes) -> None:
        """Shadow one owner's blob in the local object store: the commit binding stays, the bytes drift."""
        old = git_bytes(self.repo, "rev-parse", f"{rev}:{path}").decode().strip()
        new = subprocess.run(["git", "-C", str(self.repo), "hash-object", "-w", "--stdin"], input=content,
                             capture_output=True, check=True).stdout.decode().strip()
        subprocess.run(["git", "-C", str(self.repo), "replace", old, new], check=True, capture_output=True)

    def test_a_matching_source_selects_its_variant(self) -> None:
        with self.pinned():
            variant, proof = self.select("legacy-a", self.commits["legacy-a"])
            self.assertEqual(variant, "legacy-bootstrap")
            self.assertEqual(proof["commit"], self.commits["legacy-a"])
            self.assertEqual(proof["owners"], self.rows[0]["owners"])
            self.assertEqual(proof["fingerprint"], legacy.fingerprint_of(proof["owners"]))
            variant, proof = self.select("fixed-a", self.commits["fixed-a"])
            self.assertEqual(variant, "corrected")
            self.assertNotEqual(proof["fingerprint"], legacy.fingerprint_of(self.rows[0]["owners"]))

    def test_each_drifted_or_disconnected_owner_holds_by_name(self) -> None:
        cases = [("byte drift appends one byte", "internal/agent/mutations.go", LEGACY_SOURCES["internal/agent/mutations.go"] + "\n"),
                 ("raw CRLF drift", "internal/agent/envelope.go", LEGACY_SOURCES["internal/agent/envelope.go"].replace("\n", "\r\n")),
                 ("disconnected validator helper", "internal/agent/generated_contracts.go",
                  LEGACY_SOURCES["internal/agent/generated_contracts.go"].replace("func ValidateGeneratedPayload", "func movedValidator")),
                 ("disconnected schema binding", "internal/agent/generated_payload_schemas.go",
                  LEGACY_SOURCES["internal/agent/generated_payload_schemas.go"].replace('#/$defs/version', '#/$defs/version_v2'))]
        for path in legacy.LEGACY_OWNERS:
            if path not in {case[1] for case in cases}:
                cases.append(("owner byte drift", path, LEGACY_SOURCES[path] + "\n"))
        for name, path, content in cases:
            with self.subTest(owner=path, case=name), tempfile.TemporaryDirectory() as name2:
                self.assertNotEqual(content, LEGACY_SOURCES[path], "a drift control must actually change owner bytes")
                repo = Path(name2) / "repo"
                subprocess.run(["git", "init", "-q", "-b", "main", str(repo)], check=True, capture_output=True)
                saved_repo = self.repo
                self.repo = repo
                try:
                    commit = self.commit_tagged(LEGACY_SOURCES, "legacy-a")
                    rows = ({"tag": "legacy-a", "commit": commit, "variant": legacy.LEGACY_VARIANT,
                             "owners": {p: blob_sha256(repo, commit, p) for p in legacy.LEGACY_OWNERS}},)
                    with self.pinned(rows), self.assertRaises(legacy.LegacyProofError) as raised:
                        self.shadow_blob(commit, path, content.encode())
                        legacy.select_variant(repo, "legacy-a", commit)
                    self.assertIn(path, raised.exception.detail)
                finally:
                    self.repo = saved_repo

    def test_a_missing_owner_holds(self) -> None:
        empty = subprocess.run(["git", "-C", str(self.repo), "-c", "user.name=t", "-c", "user.email=t@example.invalid",
                                "commit-tree", "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "-m", "empty"],
                               capture_output=True, check=True, env={**os.environ, "GIT_CONFIG_NOSYSTEM": "1"}).stdout.decode().strip()
        with self.assertRaises(legacy.LegacyProofError) as raised:
            legacy.source_fingerprint(self.repo, empty)
        self.assertIn("internal/agent/mutations.go", raised.exception.detail)

    def test_a_local_or_remote_commit_mismatch_holds(self) -> None:
        with self.pinned():
            with self.assertRaises(legacy.LegacyProofError) as raised:
                self.select("legacy-a", "0" * 40)
            self.assertIn("release commit", raised.exception.detail)
            subprocess.run(["git", "-C", str(self.repo), "tag", "-f", "legacy-a", self.commits["fixed-a"]],
                           check=True, capture_output=True)
            with self.assertRaises(legacy.LegacyProofError) as raised:
                self.select("legacy-a", self.commits["legacy-a"])
            self.assertIn("checkout commit", raised.exception.detail)

    def test_an_unlisted_tag_selects_the_existing_machinery(self) -> None:
        with self.pinned():
            self.assertIsNone(self.select("unlisted", "0" * 40))

    def test_the_committed_legacy_pins_bind_this_checkout(self) -> None:
        self.assertEqual(len(legacy.LEGACY_SOURCE_PINS), 9)
        groups: dict[str, list[str]] = {}
        for row in legacy.LEGACY_SOURCE_PINS:
            commit = git_bytes(REPO, "rev-parse", f"{row['tag']}^{{commit}}").decode().strip()
            self.assertEqual(commit, row["commit"], row["tag"])
            for path in legacy.LEGACY_OWNERS:
                self.assertEqual(blob_sha256(REPO, row["tag"], path), row["owners"][path], f"{row['tag']}:{path}")
            groups.setdefault(json.dumps(row["owners"], sort_keys=True), []).append(row["tag"])
        self.assertEqual(sorted(sorted(group) for group in groups.values()),
                         [["v0.1.0", "v0.2.0"], ["v0.3.0", "v0.4.0", "v0.5.0"], ["v0.6.0", "v0.7.0", "v0.7.1"], ["v0.8.0"]])
        corrected = next(row for row in legacy.LEGACY_SOURCE_PINS if row["variant"] == legacy.CORRECTED_VARIANT)
        self.assertEqual(corrected["tag"], "v0.8.0")
        predecessor = next(row for row in legacy.LEGACY_SOURCE_PINS if row["tag"] == "v0.7.1")
        drifted = [path for path in legacy.LEGACY_OWNERS if corrected["owners"][path] != predecessor["owners"][path]]
        self.assertEqual(drifted, ["internal/agent/mutations.go"])

    def test_the_real_capture_and_message_controls_keep_their_plans(self) -> None:
        self.assertIsNone(legacy.select_variant(REPO, "v0.26.1", git_bytes(REPO, "rev-parse", "v0.26.1^{commit}").decode().strip()))
        self.assertEqual(invoke.invoke_plan(invoke.tag_facts(REPO, "v0.26.1")),
                         ("client-register", "grant", "invoke:concord_work_define.capture"))
        self.assertEqual(invoke.invoke_plan(invoke.tag_facts(REPO, "v0.27.0")),
                         ("client-register", "grant", "invoke:concord_work_define.capture", "invoke:concord_work_define.capture",
                          "invoke:concord_work_relate.message_send", "invoke:concord_work_relate.message_withdraw"))

    def test_a_bootstrap_plan_is_empty_and_runs_nothing(self) -> None:
        facts = {"has_grant": True, "has_messages": True, "bootstrap_only": True}
        self.assertEqual(invoke.invoke_plan(facts), ())
        self.assertNotEqual(invoke.invoke_plan(facts), invoke.invoke_plan({"has_grant": True, "has_messages": False}))

        class Forbidden:
            def run(self, verb: str, request: dict) -> tuple[int, str, str]:
                raise AssertionError(f"bootstrap-only must not call {verb}")

        with tempfile.TemporaryDirectory() as name:
            repo = fixture_repository(Path(name))
            case = Path(name) / "case"
            case.mkdir()
            record = invoke.run_invoke_phase(Forbidden().run, repo, "v0.37.2", case, Path(name) / "store.db", facts=dict(facts))
        self.assertEqual(record, {"outcome": "bootstrap-only", "plan": []})


if __name__ == "__main__":
    unittest.main()
