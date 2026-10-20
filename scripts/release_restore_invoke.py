#!/usr/bin/env python3
"""The approval-free invoke phase of the release restore gate.

Every released binary exposes `concord invoke`, and from v0.1.0 the operation
`concord_work_define.capture` is approval-free in its CLI enum at every tag.
Reaching it needs a registered client and, before v4.6.0, a grant minted from
an Ed25519-signed canonical assertion; the binary has no keygen, so the key
material comes from `openssl` on PATH. The signing eras, envelope shapes, and
scope-version formulas are derived from each tag's own source through the
local git checkout, never hand-pinned per release:

- assertion form A, v0.1.0-v0.39.x: `v1\0` then 14 length-framed fields,
  including client_version, surface_range (negotiated against the tag's
  supported list), and envelope_versions;
- assertion form B, v0.40.0-v4.5.x: the same framing, 11 fields;
- v4.6.0 (commit 61458098) removed the grant: invoke is authorized by the
  registered client policy alone, and the envelope carries no token.

The signed `issued_at` must be RFC3339Nano-normalized — Go drops trailing
zeros of the fractional part when it re-serializes, so a fixed six-digit
fraction fails verification on roughly one run in ten. The scope version
digest changed formulas with the membership-set projection: the tag's own
`scopeVersion` source decides which formula the harness computes.

A refusal here is a named case failure, never a skip: the gate reports the
stage that refused and the writer stays unevaluated.

The invoke plan is per-tag and lists only steps the tag's own source can run:
capture-only before `concord_work_relate.message_send` and
`concord_work_relate.message_withdraw` are registered, and two distinct
captures then send and withdraw once they are. The gate hashes the actual
plan, so no report lists an unexecuted step or reuses another variant's hash.
"""

from __future__ import annotations

import base64
import hashlib
import json
import re
import subprocess
import sqlite3
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

INVOKE_CLIENT = "gate-invoke-client"
INVOKE_PRINCIPAL = "gate-invoke-principal"
INVOKE_SESSION = "gate-invoke-session"
INVOKE_AGENT = "gate-invoke-agent"
# work_relate is offered at every tag: the capability is valid from v0.1.0 and
# message_send needs it from v0.27.0, but offering it binds no message step.
INVOKE_CAPABILITIES = ("product_read", "work_define", "work_relate")
INVOKE_PRODUCT = "gate-product"
INVOKE_PROJECT = "gate-project"
INVOKE_REMOTE = "https://example.invalid/gate/project.git"
INVOKE_TITLE = "Invoke Work"
INVOKE_MESSAGE_BODY = "gate release-restore message chain"
# The message operations, their one closed required-input set, and the input
# schema name both registrations have used since v0.27.0. A tag whose source
# shows anything else holds by name instead of guessing.
MESSAGE_OPERATIONS = {
    "message_send": (frozenset({"work_id", "body", "expected_version", "idempotency_key"}), "work_relate_message_send_input"),
    "message_withdraw": (frozenset({"work_id", "message_id", "expected_version", "idempotency_key"}), "work_relate_message_withdraw_input"),
}


class InvokeError(Exception):
    """A named invoke-phase failure: (stage, detail)."""

    def __init__(self, stage: str, detail: str):
        super().__init__(f"{stage}: {detail}")
        self.stage, self.detail = stage, detail


def _git_show(repo: Path, tag: str, path: str) -> str:
    proc = subprocess.run(["git", "-C", str(repo), "show", f"{tag}:{path}"], capture_output=True, text=True, timeout=120)
    if proc.returncode != 0:
        raise InvokeError("tag-facts", f"git show {tag}:{path} exited {proc.returncode}: {proc.stderr.strip()[-200:]}")
    return proc.stdout


def rfc3339nano(moment: datetime) -> str:
    """Format as Go's RFC3339Nano: UTC, fractional digits with trailing zeros removed."""
    text = moment.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")
    whole, fraction = text.split(".")
    fraction = fraction.rstrip("0")
    return f"{whole}.{fraction}Z" if fraction else f"{whole}Z"


def canonical_assertion(names: list[str], fields: dict[str, str]) -> bytes:
    """The signed bytes: `v1\0` then `name=<UTF-8 length>:<value>|` per field, in the tag's order."""
    body = bytearray(b"v1\x00")
    for name in names:
        value = fields.get(name, "").encode()
        body += f"{name}={len(value)}:".encode() + value + b"|"
    return bytes(body)


def keypair(case: Path) -> tuple[Path, str]:
    """An Ed25519 keypair from openssl: the private key file and the base64 raw public key.

    `openssl pkey -pubout -outform DER` emits a 44-byte Ed25519 subject public
    key info block whose last 32 bytes are the raw key the CLI registers.
    """
    key, pub = case / "invoke.pem", case / "invoke.pub"
    for argv in (["openssl", "genpkey", "-algorithm", "ed25519", "-out", str(key)],
                 ["openssl", "pkey", "-in", str(key), "-pubout", "-outform", "DER", "-out", str(pub)]):
        proc = subprocess.run(argv, capture_output=True, text=True, timeout=120)
        if proc.returncode != 0:
            raise InvokeError("keying", f"{' '.join(argv[:3])} exited {proc.returncode}: {proc.stderr.strip()[-200:]}")
    der = pub.read_bytes()
    if len(der) != 44:
        raise InvokeError("keying", f"openssl emitted a {len(der)}-byte public key, expected the 44-byte Ed25519 block")
    return key, base64.b64encode(der[-32:]).decode()


def sign(key: Path, message: bytes, case: Path) -> str:
    """Sign the raw message with `openssl pkeyutl -rawin`; returns base64."""
    inbox, outbox = case / "invoke.message", case / "invoke.sig"
    inbox.write_bytes(message)
    proc = subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(key), "-rawin", "-in", str(inbox), "-out", str(outbox)],
                          capture_output=True, text=True, timeout=120)
    if proc.returncode != 0:
        raise InvokeError("signing", f"openssl pkeyutl exited {proc.returncode}: {proc.stderr.strip()[-200:]}")
    return base64.b64encode(outbox.read_bytes()).decode()


def synthetic_repository(case: Path) -> Path:
    """A git repository with a linked worktree whose origin matches the gate's `gate-remote` locator.

    Project resolution at every tag matches the remote URL or the canonical
    path against the Project's registered locators, so the linked worktree —
    required for mutating authority — resolves through the locator the domain
    recipe already registered.
    """
    repo, worktree = case / "repositories" / "invoke", case / "repositories" / "invoke-worktree"
    if worktree.exists():
        return worktree
    for argv in (["git", "init", "-q", "-b", "main", str(repo)],
                 ["git", "-C", str(repo), "-c", "user.name=gate", "-c", "user.email=gate@example.invalid", "commit", "--allow-empty", "-m", "invoke"],
                 ["git", "-C", str(repo), "remote", "add", "origin", INVOKE_REMOTE],
                 ["git", "-C", str(repo), "worktree", "add", "-q", "-b", "invoke", str(worktree), "main"]):
        proc = subprocess.run(argv, capture_output=True, text=True, timeout=120,
                              env={"GIT_CONFIG_NOSYSTEM": "1", "PATH": "/usr/bin:/bin", "HOME": str(case / "home")})
        if proc.returncode != 0:
            raise InvokeError("repository", f"{' '.join(argv[:4])} exited {proc.returncode}: {proc.stderr.strip()[-200:]}")
    return worktree


def scope_version(formula: str, store: Path) -> str:
    """The caller-computed scope version, in the formula the tag's own store uses."""
    connection = sqlite3.connect(f"file:{store}?mode=ro", uri=True)
    try:
        if formula == "triples":
            rows = connection.execute(
                "SELECT product_id, project_id, role FROM product_projects "
                "WHERE product_id IN (SELECT product_id FROM product_projects WHERE project_id=?) "
                "ORDER BY product_id, project_id, role", (INVOKE_PROJECT,)).fetchall()
            payload = json.dumps([[row[0], row[1], row[2]] for row in rows], separators=(",", ":")).encode()
        else:
            rows = connection.execute(
                "SELECT product_id, role FROM product_projects WHERE project_id=? ORDER BY product_id, role",
                (INVOKE_PROJECT,)).fetchall()
            payload = "\n".join(f"{row[0]}|{row[1]}" for row in rows).encode()
    finally:
        connection.close()
    return "sha256:" + hashlib.sha256(payload).hexdigest()


def message_support(contracts: str, tag: str) -> bool:
    """Whether the tag's own generated registrations offer the approval-free message operations.

    Both operations absent from a recognized source is an explicit capture-only
    plan. Exactly one present, an operation that is not an approval-free
    work_relate mutation, an input schema the harness does not know, or a
    changed required-input list all hold by name.
    """
    if not re.search(r'ID: "concord_work_relate\.[a-z_]+"', contracts):
        raise InvokeError("tag-facts", f"{tag} carries no readable concord_work_relate registration")
    found = set()
    for operation, (required, schema) in MESSAGE_OPERATIONS.items():
        entry = re.search(r'\{ID: "concord_work_relate\.' + operation + r'",([^}]*)\}', contracts)
        if entry is None:
            continue
        for needle, what in (('Kind: OperationKind("mutation")', "kind"),
                             ('Capability: Capability("work_relate")', "capability"),
                             ('Approval: ApprovalClass("none")', "approval")):
            if needle not in entry.group(1):
                raise InvokeError("tag-facts", f"{tag} registers {operation} with a {what} the harness cannot invoke approval-free")
        bound = re.search(r'InputSchema: "([^"]+)"', entry.group(1))
        if bound and bound.group(1) != schema:
            raise InvokeError("tag-facts", f"{tag} binds {operation} to the input schema {bound.group(1)}, want {schema}")
        found.add(operation)
    if len(found) == 1:
        missing = next(operation for operation in MESSAGE_OPERATIONS if operation not in found)
        raise InvokeError("tag-facts", f"{tag} registers {next(iter(found))} without {missing}: partial message registration")
    if not found:
        # A source whose other work_relate rows are readable but whose message
        # surface appears in any other shape is unknown support: it refuses,
        # because a downgrade here would silently understate what ran.
        traced = next((operation for operation in MESSAGE_OPERATIONS if operation in contracts), None)
        if traced:
            raise InvokeError("tag-facts", f"{tag} references {traced} outside the registration shape the harness can read")
        return False
    for operation, (required, schema) in MESSAGE_OPERATIONS.items():
        listed = re.search('"' + schema + r'":\s*\{Required: \[\]string\{([^}]*)\}', contracts)
        if listed is None:
            raise InvokeError("tag-facts", f"{tag} lists no required inputs for {schema}")
        fields = frozenset(field.strip().strip('"') for field in listed.group(1).split(","))
        if fields != required:
            raise InvokeError("tag-facts", f"{tag} requires {sorted(fields)} for {operation}, want {sorted(required)}")
    return True


def invoke_plan(facts: dict) -> tuple[str, ...]:
    """The per-tag plan: only the steps this source can actually run, grant included only when it runs."""
    plan = ["client-register"]
    if facts["has_grant"]:
        plan.append("grant")
    plan.append("invoke:concord_work_define.capture")
    if facts["has_messages"]:
        plan += ["invoke:concord_work_define.capture",
                 "invoke:concord_work_relate.message_send",
                 "invoke:concord_work_relate.message_withdraw"]
    return tuple(plan)


def tag_facts(repo: Path, tag: str) -> dict:
    """Every tag-derived fact the invoke phase needs, read once per case.

    The grant-era boundary, the signed field list, the negotiated surface, the
    manifest digest, the registration shape, the message-operation support,
    and the scope-version formula all come from the tag's own source, so a new
    release needs no harness change until one of these surfaces moves — which
    then refuses by name here.
    """
    main_src = _git_show(repo, tag, "cmd/concord/main.go")
    authority = _git_show(repo, tag, "internal/agent/authority.go")
    names = re.search(r"names := \[\]string\{([^}]*)\}", authority)
    manifest = subprocess.run(["git", "-C", str(repo), "grep", "-h", "ManifestDigest = ", tag, "--", "internal/agent"],
                              capture_output=True, text=True, timeout=120)
    digest = re.search(r'"(sha256:[0-9a-f]{64})"', manifest.stdout) if manifest.returncode == 0 else None
    if digest is None:
        raise InvokeError("tag-facts", f"no ManifestDigest constant at {tag}")
    facts: dict = {
        "has_grant": 'Canonical: "grant"' in main_src,
        "names": [name.strip().strip('"') for name in names.group(1).split(",")] if names else [],
        "manifest_digest": digest.group(1),
        "agent_scope": "agent_scope" in main_src.split('Canonical: "client-register"')[1][:600] if 'Canonical: "client-register"' in main_src else False,
        "grant_ref": '"grant_ref"' in main_src.split('Canonical: "invoke"')[1][:600] if 'Canonical: "invoke"' in main_src else False,
    }
    if facts["has_grant"]:
        if not facts["names"]:
            raise InvokeError("tag-facts", f"{tag} has a grant command but no canonical assertion field list")
        if "surface_range" in facts["names"]:
            locate = subprocess.run(["git", "-C", str(repo), "grep", "-l", "func NegotiateSurfaceVersion", tag, "--", "internal/agent"],
                                     capture_output=True, text=True, timeout=120)
            if locate.returncode != 0 or not locate.stdout.strip():
                raise InvokeError("tag-facts", f"{tag} signs a surface range but its negotiation source is unreadable")
            semver_path = locate.stdout.strip().splitlines()[0].split(":", 1)[1]
            supported = re.findall(r"\{Major: (\d+), Minor: (\d+), Patch: (\d+)\}", _git_show(repo, tag, semver_path))
            if not supported:
                raise InvokeError("tag-facts", f"{tag} signs a surface range but exposes no negotiable surface list")
            major, minor, patch = max(supported, key=lambda parts: tuple(int(part) for part in parts))
            facts["surface"] = f"{major}.{minor}.{patch}"
    locator = _git_show(repo, tag, "internal/store/project_locator.go")
    facts["scope_formula"] = "triples" if "product_id,project_id,role" in locator else "pairs"
    facts["has_messages"] = message_support(_git_show(repo, tag, "internal/agent/generated_contracts.go"), tag)
    return facts


def invoke_steps(facts: dict, case: Path, key: Path, public: str, worktree: Path) -> tuple[list[tuple[str, dict]], dict]:
    """The registration steps and, in a grant era, the signed grant request."""
    now = datetime.now(timezone.utc)
    register = {"client_ref": INVOKE_CLIENT, "key_id": "gate-invoke-key", "principal_ref": INVOKE_PRINCIPAL,
                "public_key": public, "capabilities": list(INVOKE_CAPABILITIES),
                "product_scope": [INVOKE_PRODUCT], "project_scope": [INVOKE_PROJECT]}
    if facts["agent_scope"]:
        register["agent_scope"] = [INVOKE_AGENT]
    steps = [("client-register", register)]
    grant: dict = {}
    if facts["has_grant"]:
        fields = {"client_ref": INVOKE_CLIENT, "session_ref": INVOKE_SESSION, "agent_ref": INVOKE_AGENT,
                  "directory": str(worktree), "worktree": str(worktree), "requested_product_id": INVOKE_PRODUCT,
                  "requested_project_ids": ",".join(sorted([INVOKE_PROJECT])),
                  "requested_capabilities": ",".join(sorted(INVOKE_CAPABILITIES)),
                  "issued_at": rfc3339nano(now), "nonce": "gate-invoke-nonce-0123456789abcdef",
                  "manifest_digest": facts["manifest_digest"]}
        if "client_version" in facts["names"]:
            fields |= {"client_version": "gate-invoke-1", "surface_range": facts["surface"], "envelope_versions": "1.0"}
        assertion = dict(fields) | {"signature": sign(key, canonical_assertion(facts["names"], fields), case)}
        assertion["requested_project_ids"] = [INVOKE_PROJECT]
        assertion["requested_capabilities"] = list(INVOKE_CAPABILITIES)
        if "client_version" in facts["names"]:
            assertion |= {"surface_range": facts["surface"], "envelope_versions": "1.0"}
        grant = {"expires_at": rfc3339nano(now + timedelta(hours=1)), "max_uses": 10, "assertion": assertion}
    return steps, grant


def invoke_envelope(facts: dict, grant: dict, worktree: Path, store: Path, request_id: str) -> dict:
    """One mutation's call envelope in the era's shape, with that invocation's request id."""
    envelope = {"schema_version": "1.0", "request_id": request_id, "client_ref": INVOKE_CLIENT,
                "principal_ref": INVOKE_PRINCIPAL, "session_ref": INVOKE_SESSION, "agent_ref": INVOKE_AGENT,
                "directory": str(worktree), "worktree": str(worktree), "ambient_project_id": INVOKE_PROJECT,
                "scope_version": grant.get("scope_version") or scope_version(facts["scope_formula"], store),
                "manifest_digest": facts["manifest_digest"]}
    if facts["has_grant"]:
        envelope["grant_token" if not facts["grant_ref"] else "grant_ref"] = grant["grant_token"]
        if "client_version" in facts["names"]:
            envelope |= {"client_version": "gate-invoke-1", "surface_version": grant.get("surface_version", facts["surface"]),
                         "envelope_version": grant.get("envelope_version", "1.0")}
    return envelope


def work_ref(result: dict, stage: str) -> tuple[str, int]:
    """The single work-item changed reference a mutation result must report.

    Every era's ChangedRef.Version is a Go string (internal/agent/envelope.go,
    unchanged since v0.1.0) carrying a positive decimal, so a number or any
    other shape is a malformed ref, never a version to read past.
    """
    refs = result.get("changed_refs")
    if not isinstance(refs, list):
        raise InvokeError(stage, f"{result.get('outcome')} mutation reported no changed_refs list")
    work = [item for item in refs if isinstance(item, dict) and item.get("entity_kind") == "work_item"]
    if len(work) != 1:
        raise InvokeError(stage, f"expected exactly one work_item changed ref, got {json.dumps(refs)[:200]}")
    ident, version = work[0].get("id"), work[0].get("version")
    if not isinstance(ident, str) or not ident:
        raise InvokeError(stage, f"malformed work_item changed ref id: {json.dumps(work[0])[:200]}")
    if not isinstance(version, str) or not re.fullmatch(r"[1-9][0-9]*", version):
        raise InvokeError(stage, f"malformed work_item changed ref version: {json.dumps(work[0])[:200]}")
    return ident, int(version)


def sent_message_id(store: Path, sender: str, recipient: str) -> str:
    """The one sent row the writer's own store must show for the exact sender/recipient pair.

    Read-only: every mutation stays an invoke through the released writer.
    """
    try:
        connection = sqlite3.connect(f"file:{store}?mode=ro", uri=True)
        try:
            rows = connection.execute(
                "SELECT message_id FROM work_messages WHERE sender_work_id=? AND recipient_work_id=? AND state='sent'",
                (sender, recipient)).fetchall()
        finally:
            connection.close()
    except sqlite3.Error as error:
        raise InvokeError("message-id", f"the writer store has no readable work_messages projection: {error}") from None
    ids = [row[0] for row in rows]
    if len(ids) != 1 or not isinstance(ids[0], str) or not re.fullmatch(r"msg:[0-9a-f]{32}", ids[0]):
        raise InvokeError("message-id", f"expected exactly one well-formed sent row for {sender} -> {recipient}, got {ids!r}")
    return ids[0]


def run_invoke_phase(run, repo: Path, tag: str, case: Path, store: Path, facts: dict | None = None) -> dict:
    """Drive the invoke phase through `run(verb, request) -> (rc, out, err)`.

    Returns the phase record: the outcome and the plan actually executed, for
    the gate's report and recipe hash. `facts` may carry the tag facts a caller
    already derived. Every refusal is named by stage; the caller decides how a
    refusal classifies the writer.
    """
    facts = facts if facts is not None else tag_facts(repo, tag)
    plan = invoke_plan(facts)
    worktree = synthetic_repository(case)
    key, public = keypair(case)
    steps, grant_request = invoke_steps(facts, case, key, public, worktree)
    for verb, request in steps:
        rc, out, err = run(verb, request)
        if rc != 0:
            raise InvokeError(verb, f"exited {rc}: {(err or out).strip()[-300:]}")
    grant: dict = {}
    if facts["has_grant"]:
        rc, out, err = run("grant", grant_request)
        if rc != 0:
            raise InvokeError("grant", f"exited {rc}: {(err or out).strip()[-300:]}")
        grant = json.loads(out)
        if "grant_token" not in grant:
            raise InvokeError("grant", f"no token in grant response: {sorted(grant)}")

    def invoke(tool: str, operation: str, payload: dict, request_id: str, idempotency_key: str, stage: str) -> dict:
        envelope = invoke_envelope(facts, grant, worktree, store, request_id)
        rc, out, err = run("invoke", {"tool": tool, "operation": operation, "call_envelope": envelope,
                                      "input": payload | {"idempotency_key": idempotency_key}})
        if rc != 0:
            raise InvokeError(stage, f"{operation} exited {rc}: {(err or out).strip()[-300:]}")
        result = json.loads(out)
        if result.get("outcome") != "ok":
            raise InvokeError(stage, f"{operation} outcome {result.get('outcome')}: {json.dumps(result.get('error', {}))[:300]}")
        return result

    capture = {"title": INVOKE_TITLE, "value_statement": "invoke recipe", "kind": "task", "project_ids": [INVOKE_PROJECT]}
    sender, sender_version = work_ref(invoke("concord_work_define", "capture", capture, "gate-invoke-request-1",
                                             "gate-invoke-idempotency-1", "invoke"), "invoke")
    if not facts["has_messages"]:
        return {"outcome": "ok", "plan": list(plan)}
    recipient_capture = capture | {"title": "Invoke Recipient Work", "value_statement": "invoke recipe recipient"}
    recipient, _ = work_ref(invoke("concord_work_define", "capture", recipient_capture, "gate-invoke-request-2",
                                   "gate-invoke-idempotency-2", "capture-2"), "capture-2")
    if recipient == sender:
        raise InvokeError("capture-2", f"the second capture returned the sender's work item {sender}")
    send = {"work_id": sender, "recipient_work_id": recipient, "body": INVOKE_MESSAGE_BODY, "expected_version": sender_version}
    sent_id, sent_version = work_ref(invoke("concord_work_relate", "message_send", send, "gate-invoke-request-3",
                                            "gate-invoke-idempotency-3", "message-send"), "message-send")
    if sent_id != sender:
        raise InvokeError("message-send", f"send changed work ref {sent_id} is not the sender {sender}")
    withdraw = {"work_id": sender, "message_id": sent_message_id(store, sender, recipient), "expected_version": sent_version}
    withdrawn_id, _ = work_ref(invoke("concord_work_relate", "message_withdraw", withdraw, "gate-invoke-request-4",
                                      "gate-invoke-idempotency-4", "message-withdraw"), "message-withdraw")
    if withdrawn_id != sender:
        raise InvokeError("message-withdraw", f"withdraw changed work ref {withdrawn_id} is not the sender {sender}")
    return {"outcome": "ok", "plan": list(plan)}
