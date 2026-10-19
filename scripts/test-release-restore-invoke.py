#!/usr/bin/env python3
"""Tests for scripts/release_restore_invoke.py.

The tag facts are read from a fixture git repository built on disk, so the
eras are exercised offline against the same `git show` the gate runs. The
end-to-end case drives a fake released binary that verifies the harness's
Ed25519 signature with openssl itself, so the signed chain is proven without
downloading a release. No network and no installed store is reachable.
"""

from __future__ import annotations

import base64
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

SPEC = importlib.util.spec_from_file_location("release_restore_invoke", Path(__file__).with_name("release_restore_invoke.py"))
assert SPEC and SPEC.loader
invoke = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = invoke
SPEC.loader.exec_module(invoke)

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
SEMVER = 'func NegotiateSurfaceVersion(supportedRange string) (string, error) {\n\tsupported := []SemVer{{{Major: 3, Minor: 8, Patch: 0}, {Major: 4, Minor: 0, Patch: 0}}}\n}\n'
LOCATOR_PAIRS = "SELECT product_id,role FROM product_projects"
LOCATOR_TRIPLES = "SELECT product_id,project_id,role FROM product_projects"

# A fake released binary: a Python script that registers the client, verifies
# the assertion signature with openssl when the era has a grant, and appends a
# work row to the store for a capture. It pins the era surfaces the harness
# must produce: the canonical field list, the envelope token field, and the
# scope version.
FAKE_BINARY = """#!{python}
import base64, json, os, sqlite3, subprocess, sys, tempfile

request = json.load(sys.stdin)
store = os.environ["CONCORD_DB_PATH"]
verb = sys.argv[1]
if verb == "client-register":
    key = base64.b64decode(request["public_key"])
    assert len(key) == 32, "public key must be the raw 32 bytes"
    (open(os.environ["HOME"] + "/registered.pub", "wb")).write(key)
    print(json.dumps({"ok": True}))
    sys.exit(0)
if verb == "grant":
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
    connection.execute("insert into domain_events values ('work.created', 2)")
    connection.commit()
    connection.close()
    print(json.dumps({"outcome": "ok"}))
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
    commit(repo, {"cmd/concord/main.go": MAIN_GRANT.format(token="grant_ref", agent=""),
                  "internal/agent/authority.go": FORM_A_AUTHORITY,
                  "internal/agent/semver.go": SEMVER,
                  "internal/agent/generated_contracts.go": CONTRACTS.format(digest=digest),
                  "internal/store/project_locator.go": LOCATOR_PAIRS}, "v0.37.2")
    commit(repo, {"cmd/concord/main.go": MAIN_GRANT.format(token="grant_token", agent=""),
                  "internal/agent/authority.go": FORM_B_AUTHORITY,
                  "internal/agent/generated_contracts.go": CONTRACTS.format(digest="sha256:" + "b" * 64)}, "v0.40.0")
    commit(repo, {"cmd/concord/main.go": MAIN_NOGRANT.format(agent=', Optional: "agent_scope"'),
                  "internal/agent/authority.go": "",
                  "internal/store/project_locator.go": LOCATOR_TRIPLES,
                  "internal/agent/generated_contracts.go": CONTRACTS.format(digest="sha256:" + "c" * 64)}, "v4.6.0")
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
            early = invoke.tag_facts(repo, "v0.37.2")
            self.assertTrue(early["has_grant"])
            self.assertIn("client_version", early["names"])
            self.assertEqual(early["surface"], "4.0.0")
            self.assertEqual(early["manifest_digest"], "sha256:" + "a" * 64)
            self.assertFalse(early["agent_scope"])
            self.assertEqual(early["scope_formula"], "pairs")
            middle = invoke.tag_facts(repo, "v0.40.0")
            self.assertTrue(middle["has_grant"])
            self.assertNotIn("client_version", middle["names"])
            self.assertNotIn("surface", middle)
            self.assertEqual(middle["manifest_digest"], "sha256:" + "b" * 64)
            modern = invoke.tag_facts(repo, "v4.6.0")
            self.assertFalse(modern["has_grant"])
            self.assertTrue(modern["agent_scope"])
            self.assertEqual(modern["scope_formula"], "triples")
            self.assertEqual(modern["manifest_digest"], "sha256:" + "c" * 64)

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

            result = invoke.run_invoke_phase(run, repo, tag, case, Path(env["CONCORD_DB_PATH"]))
            self.assertEqual(result["outcome"], "ok")
            connection = sqlite3.connect(env["CONCORD_DB_PATH"])
            self.assertEqual(connection.execute("SELECT kind, payload_version FROM domain_events").fetchall(), [("work.created", 2)])
            connection.close()

    def test_the_signed_chain_each_era(self) -> None:
        for tag in ("v0.37.2", "v0.40.0", "v4.6.0"):
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


if __name__ == "__main__":
    unittest.main()
