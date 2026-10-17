#!/usr/bin/env python3
"""Packaged black-box acceptance path for the shipped Concord product.

Issue #187 part 2: the harness installs the packaged Linux amd64 binary and
adapter into isolated data/config homes, drives it through the real CLI
transport, and asserts each requirement end-to-end.

The harness is deliberately black-box. It does not import any ``internal/``
Go package, does not read the SQLite file directly except where a step asserts
on the produced artifacts, and uses only the Python standard library plus the
``concord`` binary and ``gh`` release download for the upgrade step.

Live provider boundary: no step calls a live provider. Where the test
sequence would cross a host boundary (the OpenCode agent running a prompt
through the host's model), the controlled substitute is the local concord
binary itself: it speaks the same wire contract the agent would speak, and
its execution proves that contract end-to-end. The harness therefore does
not exercise a host-side model; ``gh release download`` is a release asset
fetch from the public API, not a provider call.

Each step prints one bounded line ``PASS <name>`` or ``FAIL <name>: <detail>``.
A failure exits 1 after every attempted step has been reported; the partial
sequence gives CI a fast signal that pinpoints the broken step.

Opt-in neighbor mode: with ``--neighbors N`` the harness
also runs N background reader processes and N ordinary writer processes
against the same temporary store while the suite steps run. The neighbors
speak only the real ``concord`` CLI; they never open the SQLite file. After
the suite steps, the mode asserts zero busy and error responses, real reader
progress, and writer effects that survive with no lost and no duplicated
work items.
"""
from __future__ import annotations

import argparse
import base64
import contextlib
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from collections import Counter
from pathlib import Path

import git_environment

# CON-896: this harness builds temporary Git repositories, so a hook that
# launched it must not keep a redirecting Git namespace in place.
git_environment.scrub_inherited()

REPO_ROOT = Path(__file__).resolve().parents[1]
ADAPTER_SRC = REPO_ROOT / "adapter" / "opencode"
MANIFEST_DIGEST_FILE = REPO_ROOT / "contracts" / "agent-tool-surface.digest"
ASSERTS_OUTPUT: list[str] = []


def _preferred_temp_root() -> str:
    """Pick a parent dir for the harness scratch tree that is not /tmp.

    CI workers mount /tmp with a small inode quota; the harness's work
    directory holds the build artifacts and `go build`'s per-package work
    directories, so a constrained /tmp trips inode limits mid-build. Prefer
    the operator-overridden ``CONCORD_BLACKBOX_TMPDIR`` when set, and fall
    back to ``$XDG_RUNTIME_DIR`` (always writable, agent-scoped) before
    /tmp — neither contains a personal path, and either survives the build.
    """
    for candidate in (
        os.environ.get('CONCORD_BLACKBOX_TMPDIR'),
        os.environ.get('XDG_RUNTIME_DIR'),
        '/tmp',
    ):
        if not candidate:
            continue
        try:
            os.makedirs(candidate, exist_ok=True)
            probe = os.path.join(candidate, '.concord-blackbox-probe')
            with open(probe, 'w', encoding='utf-8') as probe_file:
                probe_file.write('ok')
            os.remove(probe)
            return candidate
        except OSError:
            continue
    return '/tmp'


# ---------------------------------------------------------------------------
# Pure-Python Ed25519 public-key derivation. The harness registers a client
# key without importing the Go key helpers.
# ---------------------------------------------------------------------------

_P = 2**255 - 19
_L = (2**252) + 27742317777372353535851937790883648493
_D = (-121665 * pow(121666, _P - 2, _P)) % _P
_NEUTRAL = (0, 1, 1, 0)


def _inv(x: int) -> int:
    return pow(x, _P - 2, _P)


def _decodepoint(s: bytes) -> tuple[int, int, int, int]:
    y = int.from_bytes(s, 'little')
    sign = y >> 255
    y &= (1 << 255) - 1
    # Curve: -x^2 + y^2 = 1 + d*x^2*y^2  =>  x^2 = (y^2 - 1) / (1 - d*y^2)  mod p
    ysq = (y * y) % _P
    xnum = (ysq - 1) % _P
    xden = _inv((1 + _D * ysq) % _P)
    x_sq = (xnum * xden) % _P
    # Candidate root for x; candidate^((p+3)/8) is a square root when one exists.
    t = pow(x_sq, (_P + 3) // 8, _P)
    if (t * t) % _P != x_sq:
        t = (t * pow(2, (_P - 1) // 4, _P)) % _P
    if (t * t) % _P != x_sq:
        raise ValueError("ed25519: no point decodes")
    if t & 1 != sign:
        t = _P - t
    return (t, y, 1, (t * y) % _P)


def _encodepoint(P: tuple[int, int, int, int]) -> bytes:
    x, y, z, _t = P
    zi = _inv(z)
    x = (x * zi) % _P
    y = (y * zi) % _P
    raw = bytearray(y.to_bytes(32, 'little'))
    raw[31] |= (x & 1) << 7
    return bytes(raw)


def _point_add(P: tuple[int, int, int, int], Q: tuple[int, int, int, int]) -> tuple[int, int, int, int]:
    # Unified addition for twisted Edwards curves (Hisil-Wong-Carter-Dawson).
    # Ed25519 uses a*x^2 + y^2 = 1 + d*x^2*y^2 with a = -1, so:
    #   H = B - a*A = B + A
    x1, y1, z1, t1 = P
    x2, y2, z2, t2 = Q
    A = (x1 * x2) % _P
    B = (y1 * y2) % _P
    C = (t1 * _D * t2) % _P
    D = (z1 * z2) % _P
    E = ((x1 + y1) * (x2 + y2) - A - B) % _P
    F = (D - C) % _P
    G = (D + C) % _P
    H = (B + A) % _P
    return ((E * F) % _P, (G * H) % _P, (F * G) % _P, (E * H) % _P)


def _point_mul(s: int, P: tuple[int, int, int, int]) -> tuple[int, int, int, int]:
    Q = _NEUTRAL
    while s > 0:
        if s & 1:
            Q = _point_add(Q, P)
        P = _point_add(P, P)
        s >>= 1
    return Q


# Base point decoded from the RFC 8032 §5.1.3 wire form (sign bit set so y is
# the 4/5 solution; the higher x root is the alternative base point but only
# this one is used in the wire protocol).
_B = _decodepoint(bytes.fromhex('5866666666666666666666666666666666666666666666666666666666666666'))


def _clamp_scalar(a_bytes: bytes) -> int:
    a = int.from_bytes(a_bytes, 'little')
    a &= (1 << 254) - 8
    a |= (1 << 254)
    return a


def _publickey(seed: bytes) -> bytes:
    h = hashlib.sha512(seed).digest()
    a = _clamp_scalar(h[:32])
    A = _point_mul(a, _B)
    return _encodepoint(A)


# ---------------------------------------------------------------------------
# Concord CLI driver. Each invocation is its own subprocess so the restart /
# reopen property (CD-0017 D4, stored-event survival) is exercised end-to-end.
# ---------------------------------------------------------------------------


class HarnessError(RuntimeError):
    """One harness step reported FAIL; carrying a human-readable detail."""


def _report(state: str, name: str, detail: str = "") -> bool:
    line = f"{state} {name}" + (f": {detail}" if detail else "")
    ASSERTS_OUTPUT.append(line)
    print(line, flush=True)
    return state == "PASS"


def _run(binary: Path, args: list[str], stdin_payload: dict | None, env: dict, timeout: int = 60) -> dict:
    """Run the binary as a subprocess with JSON stdin; return parsed JSON stdout."""
    payload = json.dumps(stdin_payload, separators=(',', ':')).encode('utf-8') if stdin_payload is not None else b''
    completed = subprocess.run(
        [str(binary), *args],
        input=payload,
        capture_output=True,
        env=env,
        timeout=timeout,
        check=False,
    )
    stdout = completed.stdout.decode('utf-8', errors='replace')
    stderr = completed.stderr.decode('utf-8', errors='replace')
    if completed.returncode != 0:
        raise HarnessError(f"exit={completed.returncode}; stderr={stderr.strip()[:400]}")
    if not stdout.strip():
        raise HarnessError(f"empty stdout; stderr={stderr.strip()[:400]}")
    try:
        return json.loads(stdout.splitlines()[-1])
    except json.JSONDecodeError as exc:
        raise HarnessError(f"stdout is not JSON: {exc}; raw={stdout[:200]!r}") from exc


def _isolated_env(work: Path) -> dict:
    """Build an isolated env: XDG homes pinned to the work directory.

    The harness runs every CLI invocation in its own subprocess with a fresh
     env block so that operator setup and invokes each go through
    process startup, library init, and SQLite open — the restart/reopen
    property CD-0017 D4 names is exercised by construction.
    """
    env = os.environ.copy()
    env.pop('CONCORD_DB_PATH', None)  # never override; the binary derives the XDG path
    env['XDG_DATA_HOME'] = str(work / 'data')
    env['XDG_CONFIG_HOME'] = str(work / 'config')
    # Build TMPDIR for `go build` — keep intermediate artifacts inside <work>
    # so this script does not pressure the host's /tmp.
    env['TMPDIR'] = str(work / 'tmp')
    env['PATH'] = '/usr/bin:/bin:/usr/local/bin' + os.pathsep + env.get('PATH', '')
    (work / 'data').mkdir(parents=True, exist_ok=True)
    (work / 'config').mkdir(parents=True, exist_ok=True)
    (work / 'tmp').mkdir(parents=True, exist_ok=True)
    return env


def _build_binary(work: Path) -> Path:
    """Build the packaged form: CGO_ENABLED=0, linux/amd64, stamped version."""
    out = work / 'bin' / 'concord'
    out.parent.mkdir(parents=True, exist_ok=True)
    cmd = [
        'go', 'build', '-trimpath', '-buildvcs=false',
        '-ldflags', '-X github.com/sharper-flow/concord/internal/version.Value=blackbox-acceptance',
        '-o', str(out), './cmd/concord',
    ]
    env = os.environ.copy()
    env['CGO_ENABLED'] = '0'
    env['GOOS'] = 'linux'
    env['GOARCH'] = 'amd64'
    env['TMPDIR'] = str(work / 'tmp')
    env['GOTMPDIR'] = str(work / 'tmp')
    (work / 'tmp').mkdir(parents=True, exist_ok=True)
    completed = subprocess.run(cmd, cwd=str(REPO_ROOT), env=env, capture_output=True, check=False, timeout=180)
    if completed.returncode != 0:
        raise HarnessError(f"go build failed: {completed.stderr.decode('utf-8', errors='replace')[:400]}")
    if not out.is_file():
        raise HarnessError("go build produced no binary")
    return out


def _stage_adapter(work: Path) -> Path:
    """Mirror adapter/opencode into <work>/adapter/opencode for the install step."""
    dest = work / 'adapter' / 'opencode'
    if dest.exists():
        shutil.rmtree(dest)
    shutil.copytree(ADAPTER_SRC, dest)
    return dest


def _b64(data: bytes) -> str:
    return base64.b64encode(data).decode('ascii')


def _git_init(work: Path) -> tuple[Path, Path]:
    """Build a git repository and return its canonical and linked-worktree paths.

    CD-0008 D1 forbids mutation authority on the main checkout; the binary
    refuses mutating invocations with non-``product_read`` capabilities when the resolved
    directory is the repository's primary worktree. The harness creates a
    linked worktree under <work>/git-worktree, so the invocation sees a non-main
    worktree and the mutation capabilities (work_define, work_transition) clear
    the firewall.
    """
    base = work / 'git-base'
    linked = work / 'git-worktree'
    base.mkdir(parents=True, exist_ok=True)
    subprocess.run(['git', 'init', '--quiet', '--initial-branch', 'main', str(base)], check=True)
    for key, value in (
        ('user.email', 'harness@concord'),
        ('user.name', 'blackbox-harness'),
        ('commit.gpgsign', 'false'),
    ):
        subprocess.run(['git', '-C', str(base), 'config', key, value], check=True)
    # Need an initial commit so HEAD resolves and a worktree can branch from it.
    readme = base / 'README.md'
    readme.write_text('harness-fixture\n', encoding='utf-8')
    subprocess.run(['git', '-C', str(base), 'add', 'README.md'], check=True)
    subprocess.run(['git', '-C', str(base), 'commit', '--quiet', '-m', 'harness-fixture'], check=True)
    # Add a sibling worktree on a fresh branch. ``git worktree add`` of an
    # absolute path with --detach gives a clean checkout independent of the
    # main one, so the core's --git-common-dir vs --git-dir check returns false
    # (this is a linked worktree, not the main one).
    subprocess.run(
        ['git', '-C', str(base), 'worktree', 'add', '--detach', str(linked), 'HEAD'],
        check=True,
    )
    for key, value in (
        ('user.email', 'harness@concord'),
        ('user.name', 'blackbox-harness'),
        ('commit.gpgsign', 'false'),
    ):
        subprocess.run(['git', '-C', str(linked), 'config', key, value], check=True)
    return base, linked


def _assert_envelope_ok(response: dict, where: str) -> None:
    outcome = response.get('outcome')
    if outcome != 'ok':
        err = response.get('error') or {}
        raise HarnessError(f"{where}: outcome={outcome}; error={json.dumps(err)[:300]}")


def _step(name: str, fn) -> bool:
    try:
        fn()
    except HarnessError as exc:
        return _report('FAIL', name, str(exc))
    except Exception as exc:  # noqa: BLE001 — surface unexpected failure modes
        return _report('FAIL', name, f"{type(exc).__name__}: {exc}")
    return _report('PASS', name)


# ---------------------------------------------------------------------------
# The end-to-end sequence. Each helper carries the #187 requirement it covers.
# ---------------------------------------------------------------------------


def step_a_version(binary: Path, env: dict) -> None:
    """#187 packaged Linux amd64 binary carries its stamped version string."""
    completed = subprocess.run([str(binary), '--version'], capture_output=True, env=env, timeout=15, check=False)
    if completed.returncode != 0:
        raise HarnessError(f"--version exit={completed.returncode}; stderr={completed.stderr.decode('utf-8', errors='replace')[:200]}")
    if completed.stdout.decode('utf-8', errors='replace').strip() != 'blackbox-acceptance':
        raise HarnessError(f"version stamp mismatch: {completed.stdout!r}")


def step_b_bootstrap(binary: Path, env: dict, repo: Path, public_key: bytes) -> dict:
    """#187 bootstrap: register the client, create the Products, and exercise membership verbs."""
    # client register — the verb that sets the client policy used by authorization
    register = _run(binary, ['client', 'register'], {
        'client_ref': 'harness-client',
        'key_id': 'harness-key-1',
        'principal_ref': 'harness-operator',
        'public_key': _b64(public_key),
        'capabilities': ['product_read', 'work_define', 'work_transition'],
        'product_scope': ['harness-product'],
        'project_scope': ['harness-project-1', 'harness-project-2', 'harness-project-3'],
        'agent_scope': ['harness-agent-1'],
    }, env)
    if not register.get('ok'):
        raise HarnessError(f"client register response missing ok: {json.dumps(register)[:300]}")
    # product create — establishes product-1 + initial project-1 (primary)
    create = _run(binary, ['product', 'create'], {
        'product_id': 'harness-product',
        'display_name': 'Harness product',
        'stage_maturity': 'prototype',
        'stage_audience_commitment': 'operator_only',
        'project_id': 'harness-project-1',
        'project_display_name': 'Harness project 1',
        'role': 'primary',
        'reason': 'blackbox-harness bootstrap',
    }, env)
    product_version_after_create = _changed_ref_version(create, 'product', 'harness-product')
    project_version_after_create = _changed_ref_version(create, 'project', 'harness-project-1')
    # project create — adds project-2 as secondary on product-1
    project_create = _run(binary, ['project', 'create'], {
        'project_id': 'harness-project-2',
        'display_name': 'Harness project 2',
        'product_id': 'harness-product',
        'role': 'secondary',
        'expected_product_version': product_version_after_create,
        'reason': 'blackbox-harness bootstrap',
    }, env)
    product_version_after_project_create = _changed_ref_version(project_create, 'product', 'harness-product')
    # product project-add — the only happy-path recipe is to add an existing
    # project to a *different* product, since PRIMARY KEY(product_id, project_id)
    # plus a one-primary-per-product partial index prevents re-adding a project
    # to a product it is already in. Add project-2 to a second product.
    second_product = _run(binary, ['product', 'create'], {
        'product_id': 'harness-product-secondary',
        'display_name': 'Harness product secondary',
        'stage_maturity': 'prototype',
        'stage_audience_commitment': 'operator_only',
        'project_id': 'harness-project-3',
        'project_display_name': 'Harness project 3',
        'role': 'primary',
        'reason': 'blackbox-harness second product',
    }, env)
    second_product_version = _changed_ref_version(second_product, 'product', 'harness-product-secondary')
    project_version_3 = _changed_ref_version(second_product, 'project', 'harness-project-3')
    add = _run(binary, ['product', 'project-add'], {
        'product_id': 'harness-product-secondary',
        'project_id': 'harness-project-2',
        'role': 'secondary',
        'expected_version': second_product_version,
        'reason': 'blackbox-harness exercises product project-add',
    }, env)
    final_secondary_product_version = _changed_ref_version(add, 'product', 'harness-product-secondary')
    # locator-add so the invocation directory resolves to harness-project-1
    locator_add = _run(binary, ['project', 'locator-add'], {
        'project_id': 'harness-project-1',
        'locator_id': 'harness-repo',
        'kind': 'canonical_path',
        'value': str(repo),
        'expected_version': project_version_after_create,
    }, env)
    if not locator_add.get('ok'):
        raise HarnessError(f"locator-add response missing ok: {json.dumps(locator_add)[:300]}")
    return {
        'product_version': product_version_after_project_create,
        'project_version': project_version_after_create + 1,
        'secondary_product_version': final_secondary_product_version,
        'project3_version': project_version_3 + 1,
    }


def _changed_ref_version(response: dict, entity_kind: str, ref_id: str) -> int:
    for ref in response.get('changed_refs', []) or []:
        if ref.get('entity_kind') == entity_kind and ref.get('id') == ref_id:
            return int(ref['version'])
    raise HarnessError(f"missing changed_ref for {entity_kind}/{ref_id}: {json.dumps(response)[:300]}")


def step_c_context(binary: Path, env: dict, repo: Path) -> dict:
    """#187 context: resolve the Project and membership watermark for invokes."""
    context = _run(binary, ['project-resolve'], {'directory': str(repo), 'worktree': str(repo)}, env)
    for required_key in ('project_id', 'product_ids', 'scope_version'):
        if not context.get(required_key):
            raise HarnessError(f"context response missing {required_key}: {json.dumps(context)[:300]}")
    if not isinstance(context['product_ids'], list):
        raise HarnessError(f"context product_ids is not an array: {json.dumps(context)[:300]}")
    return context


def step_d_read(binary: Path, env: dict, repo: Path, context: dict, manifest_digest: str) -> None:
    """#187 at least one read: invoke concord_product_view.resolve and assert ok."""
    invoke = _run(binary, ['invoke'], {
        'call_envelope': {
            'schema_version': '1.0',
            'request_id': 'harness-read-1',
            'client_ref': 'harness-client',
            'principal_ref': '',
            'session_ref': 'harness-session-1',
            'agent_ref': 'harness-agent-1',
            'directory': str(repo),
            'worktree': str(repo),
            'ambient_project_id': context['project_id'],
            'selected_product_id': 'harness-product',
            'scope_version': context['scope_version'],
            'manifest_digest': manifest_digest,
        },
        'tool': 'concord_product_view',
        'operation': 'resolve',
        'input': {'project_id': 'harness-project-1'},
    }, env)
    _assert_envelope_ok(invoke, 'concord_product_view.resolve')


def step_e_mutation(binary: Path, env: dict, repo: Path, context: dict, manifest_digest: str) -> str:
    """#187 authorized mutation: invoke concord_work_define.capture and capture the identity."""
    invoke = _run(binary, ['invoke'], {
        'call_envelope': {
            'schema_version': '1.0',
            'request_id': 'harness-mutation-1',
            'client_ref': 'harness-client',
            'principal_ref': '',
            'session_ref': 'harness-session-1',
            'agent_ref': 'harness-agent-1',
            'directory': str(repo),
            'worktree': str(repo),
            'ambient_project_id': context['project_id'],
            'selected_product_id': 'harness-product',
            'scope_version': context['scope_version'],
            'manifest_digest': manifest_digest,
        },
        'tool': 'concord_work_define',
        'operation': 'capture',
        'input': {
            'title': 'Harness-issued work item',
            'value_statement': 'The black-box harness exercises an authorized work capture.',
            'kind': 'task',
            'project_ids': ['harness-project-1'],
            'priority': 0,
            'urgency': 'standard',
            'workflow_type_ref': 'workflow.implementation',
            'idempotency_key': 'harness-capture-0001',
        },
    }, env)
    _assert_envelope_ok(invoke, 'concord_work_define.capture')
    refs = invoke.get('changed_refs') or []
    if not refs:
        raise HarnessError(f"capture did not return changed_refs: {json.dumps(invoke)[:300]}")
    work_id = refs[0].get('id')
    work_version = int(refs[0].get('version', '0'))
    if not work_id.startswith('work-'):
        raise HarnessError(f"capture changed_ref id is not a work identity: {work_id!r}")
    if work_version < 4:
        raise HarnessError(f"capture changed_ref version={work_version}, want >=4 (workflow initialized)")
    return work_id


def step_f_workflow_evidence(binary: Path, env: dict, repo: Path, context: dict, manifest_digest: str, work_id: str, work_version: int) -> None:
    """#187 workflow execution evidence: invoke concord_work_transition.workflow_action."""
    invoke = _run(binary, ['invoke'], {
        'call_envelope': {
            'schema_version': '1.0',
            'request_id': 'harness-workflow-1',
            'client_ref': 'harness-client',
            'principal_ref': '',
            'session_ref': 'harness-session-1',
            'agent_ref': 'harness-agent-1',
            'directory': str(repo),
            'worktree': str(repo),
            'ambient_project_id': context['project_id'],
            'selected_product_id': 'harness-product',
            'scope_version': context['scope_version'],
            'manifest_digest': manifest_digest,
        },
        'tool': 'concord_work_transition',
        'operation': 'workflow_action',
        'input': {
            'work_id': work_id,
            'expected_version': work_version,
            'action_id': 'record_proposal',
            'fields': {
                'problem': 'The harness records a proposal to prove the workflow evidence path fired.',
                'affected': ['The packaged acceptance harness.'],
                'stakes': 'Without the document the proposal step records no intent.',
                'user_outcomes': ['The harness reads a recorded proposal document.'],
            },
            'idempotency_key': 'harness-workflow-action-0001',
        },
    }, env)
    # workflow_action may complete synchronously (ok) or land in pending (the
    # action is queued behind an external-effect fence); either proves the
    # workflow evidence path fired and wrote to the event log.
    outcome = invoke.get('outcome')
    if outcome not in ('ok', 'pending', 'partial'):
        raise HarnessError(f"workflow_action outcome={outcome}; full={json.dumps(invoke)[:300]}")
    if outcome in ('pending', 'partial'):
        op_ref = invoke.get('operation_ref') or {}
        if not op_ref.get('id'):
            raise HarnessError(f"workflow_action returned {outcome} without operation_ref.id: {json.dumps(invoke)[:300]}")


def step_g_restart_reopen(binary: Path, env: dict, repo: Path, context: dict, manifest_digest: str, work_id: str) -> None:
    """#187 restart/reopen: a fresh process reads the persisted work item back."""
    invoke = _run(binary, ['invoke'], {
        'call_envelope': {
            'schema_version': '1.0',
            'request_id': 'harness-restart-1',
            'client_ref': 'harness-client',
            'principal_ref': '',
            'session_ref': 'harness-session-1',
            'agent_ref': 'harness-agent-1',
            'directory': str(repo),
            'worktree': str(repo),
            'ambient_project_id': context['project_id'],
            'selected_product_id': 'harness-product',
            'scope_version': context['scope_version'],
            'manifest_digest': manifest_digest,
        },
        'tool': 'concord_work_browse',
        'operation': 'list',
        'input': {
            'project_ids': ['harness-project-1'],
            'work_ids': [work_id],
            'page': {'cursor': '', 'limit': 10},
        },
    }, env)
    _assert_envelope_ok(invoke, 'concord_work_browse.list')
    result = invoke.get('result') or {}
    items = result.get('items') or []
    if not items:
        raise HarnessError(f"work_browse.list returned no items for {work_id}; full response: {json.dumps(invoke)}")
    if items[0].get('id') != work_id:
        raise HarnessError(f"work_browse.list returned id={items[0].get('id')}, want {work_id}; full response: {json.dumps(invoke)}")


def step_h_backup_restore(binary: Path, env: dict, work: Path, repo: Path, context: dict, manifest_digest: str, work_id: str) -> None:
    """#187 backup + restore: the snapshot survives process restart and the XDG-derived open path."""
    backup_dir = work / 'backup'
    backup_dir.mkdir(parents=True, exist_ok=True)
    backup_path = backup_dir / 'snapshot.db'
    backup_resp = _run(binary, ['backup'], {'destination': str(backup_path)}, env)
    if backup_resp.get('schema_version', 0) < 1:
        raise HarnessError(f"backup manifest schema_version={backup_resp.get('schema_version')}")
    if backup_resp.get('integrity_check') != 'ok':
        raise HarnessError(f"backup integrity_check={backup_resp.get('integrity_check')}")
    # Restore into a brand-new XDG home that contains the freshly restored
    # SQLite file at the exact path the binary will derive from
    # XDG_DATA_HOME: <XDG_DATA_HOME>/concord/concord.db. The restore step
    # also rebuilds projections from the event log; the shared restore
    # verifier fails the restore on a projection invariant mismatch, while
    # the report's integrity_check describes the verified source snapshot
    # only.
    restored_home = work / 'restored-home'
    restored_db_dir = restored_home / 'concord'
    restored_db_dir.mkdir(parents=True, exist_ok=True)
    restore_dest = restored_db_dir / 'concord.db'
    restore_resp = _run(binary, ['restore'], {'source': str(backup_path), 'destination': str(restore_dest)}, env)
    source_snapshot = restore_resp.get('source_snapshot')
    if not isinstance(source_snapshot, dict):
        raise HarnessError(f"restore source_snapshot missing or not an object: {source_snapshot!r}")
    if source_snapshot.get('integrity_check') != 'ok':
        raise HarnessError(f"restore source_snapshot integrity_check={source_snapshot.get('integrity_check')}")
    if source_snapshot.get('snapshot_id') != backup_resp.get('snapshot_id'):
        raise HarnessError(f"restore source_snapshot snapshot_id={source_snapshot.get('snapshot_id')} differs from backup")
    if not restore_dest.is_file():
        raise HarnessError(f"restore destination file missing: {restore_dest}")
    # Fresh-process read in the restored home uses the same client policy and
    # resolves the restored repository context before it queries the work item.
    restored_env = dict(env)
    restored_env['XDG_DATA_HOME'] = str(restored_home)
    invoke = _run(binary, ['invoke'], {
        'call_envelope': {
            'schema_version': '1.0',
            'request_id': 'harness-restored-read-1',
            'client_ref': 'harness-client',
            'principal_ref': '',
            'session_ref': 'harness-session-1',
            'agent_ref': 'harness-agent-1',
            'directory': str(repo),
            'worktree': str(repo),
            'ambient_project_id': context['project_id'],
            'selected_product_id': 'harness-product',
            'scope_version': context['scope_version'],
            'manifest_digest': manifest_digest,
        },
        'tool': 'concord_work_browse',
        'operation': 'list',
        'input': {
            'project_ids': ['harness-project-1'],
            'work_ids': [work_id],
            'page': {'cursor': '', 'limit': 10},
        },
    }, restored_env)
    _assert_envelope_ok(invoke, 'restored concord_work_browse.list')
    result = invoke.get('result') or {}
    items = result.get('items') or []
    if not items:
        raise HarnessError(f"restored work_browse.list returned no items for {work_id}; full response: {json.dumps(invoke)}")
    if items[0].get('id') != work_id:
        raise HarnessError(f"restored work_browse.list returned id={items[0].get('id')}, want {work_id}")


def _download_release_binary(work: Path, subdir: str, tag: str | None) -> Path | None:
    """Download a release binary into work/subdir; None with a printed SKIP reason."""
    target = work / subdir
    target.mkdir(parents=True, exist_ok=True)
    command = ['gh', 'release', 'download']
    if tag is not None:
        command.append(tag)
    command += ['--repo', 'Sharper-Flow/concord', '--pattern', 'concord-v*', '--dir', str(target), '--skip-existing']
    try:
        completed = subprocess.run(command, capture_output=True, text=True, timeout=120, check=False)
    except FileNotFoundError:
        print("SKIP-UPGRADE: gh CLI not installed", flush=True)
        return None
    except subprocess.TimeoutExpired:
        print("SKIP-UPGRADE: gh release download timed out", flush=True)
        return None
    label = tag or 'latest'
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or '').strip().splitlines()
        print(f"SKIP-UPGRADE[{label}]: gh exit={completed.returncode}; {detail[-1] if detail else 'no output'}", flush=True)
        return None
    # The release pipeline writes the binary as ``concord-$VERSION`` (no
    # extension); the SBOM and SHA-256 carry extensions. Pick the only file
    # matching the binary shape: at least 1 MiB.
    for asset in sorted(target.glob('concord-v*')):
        if not asset.is_file():
            continue
        if asset.name.endswith(('.sha256', '.sbom.spdx.json', '.tar.gz')):
            continue
        if asset.stat().st_size < (1 << 20):
            continue
        os.chmod(asset, 0o755)
        return asset
    print(f"SKIP-UPGRADE[{label}]: downloaded assets are not a concord binary", flush=True)
    return None


def _upgrade_probe(binary: Path, env: dict, work: Path, subdir: str, tag: str | None, seed: bytes, public_key: bytes) -> None:
    """Open and further write, with the binary under test, a home written by
    one published release binary (latest when tag is None).

    Direction matters: an upgrade runs the NEW binary against OLD data. The
    probe bootstraps a fresh home with the release binary, then the binary
    under test backs that home up — the online-backup API reads every page,
    so a schema it cannot open fails here, not at --version, which never
    opens the store — and then creates a Product in the same home, proving a
    write after upgrade. If the old binary rejects the bootstrap payloads
    (strict-field CLI drift), the step skips with that typed failure in the
    reason: CLI-contract drift is governed by the agent-surface contracts,
    not by this store-compatibility gate. A failure of the NEW binary against
    old-binary data is a hard failure.
    """
    candidate = _download_release_binary(work, subdir, tag)
    if candidate is None:
        return
    version_check = subprocess.run([str(candidate), '--version'], capture_output=True, text=True, timeout=15, check=False)
    if version_check.returncode != 0:
        raise HarnessError(f"downloaded binary failed --version: {version_check.stderr[:200]}")
    if not version_check.stdout.strip():
        raise HarnessError("downloaded binary produced empty version string")

    # A separate home, written by the OLD binary.
    old_env = {
        **env,
        'XDG_DATA_HOME': str(work / f'{subdir}-data'),
        'XDG_CONFIG_HOME': str(work / f'{subdir}-config'),
    }
    for directory in (work / f'{subdir}-data', work / f'{subdir}-config'):
        directory.mkdir(parents=True, exist_ok=True)
    try:
        _run(candidate, ['client', 'register'], {
            'client_ref': 'harness-client',
            'key_id': 'harness-key-1',
            'principal_ref': 'harness-operator',
            'public_key': _b64(public_key),
            'capabilities': ['product_read'],
            'product_scope': ['harness-upgrade-product'],
            'project_scope': ['harness-upgrade-project'],
        }, old_env)
        _run(candidate, ['product', 'create'], {
            'product_id': 'harness-upgrade-product',
            'display_name': 'Upgrade probe product',
            'stage_maturity': 'prototype',
            'stage_audience_commitment': 'operator_only',
            'project_id': 'harness-upgrade-project',
            'project_display_name': 'Upgrade probe project',
            'role': 'primary',
            'reason': 'blackbox upgrade probe written by the release binary',
        }, old_env)
    except HarnessError as error:
        reason = str(error)[:300]
        print(f"SKIP-UPGRADE[{tag or 'latest'}]: release binary rejected the bootstrap payload (CLI drift): {reason}", flush=True)
        return

    # The NEW binary applies pending migrations through the explicit upgrade
    # verb (CD-0111 D3): open alone refuses a pending breaking migration with
    # upgrade_required rather than applying it. Only then does it read the
    # entire database through the backup verb — a schema it still cannot open
    # fails there with schema_unsupported, not later.
    _run(binary, ['upgrade'], {}, old_env)
    _run(binary, ['backup'], {'destination': str(work / f'{subdir}-after-backup')}, old_env)
    # And it writes to the upgraded home.
    _run(binary, ['product', 'create'], {
        'product_id': 'harness-upgrade-product-new',
        'display_name': 'Post-upgrade write probe',
        'stage_maturity': 'prototype',
        'stage_audience_commitment': 'operator_only',
        'project_id': 'harness-upgrade-project-new',
        'project_display_name': 'Post-upgrade write project',
        'role': 'primary',
        'reason': 'blackbox upgrade probe written by the binary under test',
    }, old_env)


# v0.13.0 is the pinned old-release probe: its databases record migration
# checksums from texts later edited (digest correction, reformatting), so it
# exercises the shipped-variant acceptance at the manifest check. Probing the
# latest release alone cannot see that class.
UPGRADE_PROBE_OLD_TAG = 'v0.13.0'


def step_i_upgrade(binary: Path, env: dict, work: Path, repo: Path, seed: bytes, public_key: bytes) -> None:
    """#187 upgrade compatibility against two published releases: the latest
    and a pinned old tag. See _upgrade_probe for the per-release contract.
    """
    _upgrade_probe(binary, env, work, 'upgrade-latest', None, seed, public_key)
    _upgrade_probe(binary, env, work, 'upgrade-old', UPGRADE_PROBE_OLD_TAG, seed, public_key)


# ---------------------------------------------------------------------------
# Opt-in neighbor mode. N background reader processes and N
# ordinary writer processes run real ``concord`` CLI invocations against the
# same temporary store while the suite steps run. The mode is off unless
# --neighbors is given. Neighbors never open the SQLite file; every check is
# derived from CLI responses and CLI read-back.
# ---------------------------------------------------------------------------

NEIGHBOR_MAX_NEIGHBORS = 8
NEIGHBOR_MAX_WRITES = 50
# Bounded reader work: a reader exits on the stop file or at this cap,
# whichever comes first, so cleanup stays deterministic.
NEIGHBOR_READER_ITERATION_CAP = 2000
# Per-child join budget after the stop file appears. One in-flight CLI call
# is bounded by the _run timeout; anything past this budget is killed and
# recorded as a problem.
NEIGHBOR_JOIN_TIMEOUT_SECONDS = 90.0

_BUSY_FAILURE_RE = re.compile(
    r"sqlite_busy|sqlite_locked|database is locked|database table is locked",
    re.IGNORECASE,
)


def _parse_args(argv: list[str]) -> argparse.Namespace:
    """Parse harness arguments. With no arguments the mode is off and the
    suite runs exactly as the CI step invokes it."""
    parser = argparse.ArgumentParser(
        prog='test-blackbox-acceptance.py',
        description='Packaged black-box acceptance harness for the shipped Concord product.',
    )
    parser.add_argument(
        '--neighbors', type=int, default=0,
        help='run N background concord readers and N ordinary writers beside the suite (default: off)',
    )
    parser.add_argument(
        '--neighbor-writes', type=int, default=4,
        help='captures per neighbor writer when --neighbors is on (default: 4)',
    )
    parser.add_argument(
        '--neighbor-worker', default=None, help=argparse.SUPPRESS,
    )
    args = parser.parse_args(argv)
    if args.neighbor_worker is None:
        if not 0 <= args.neighbors <= NEIGHBOR_MAX_NEIGHBORS:
            parser.error(f"--neighbors must be between 0 and {NEIGHBOR_MAX_NEIGHBORS}")
        if not 1 <= args.neighbor_writes <= NEIGHBOR_MAX_WRITES:
            parser.error(f"--neighbor-writes must be between 1 and {NEIGHBOR_MAX_WRITES}")
    return args


def _classify_cli_failure(detail: str) -> str:
    """Classify one failed CLI invocation: 'busy' lock contention or 'error'."""
    return 'busy' if _BUSY_FAILURE_RE.search(detail) else 'error'


def _neighbor_key(writer_index: int, sequence: int) -> str:
    """The unique idempotency key for one writer's one capture."""
    return f"harness-neighbor-w{writer_index:02d}-n{sequence:03d}"


def _neighbor_title(writer_index: int, sequence: int) -> str:
    return f"Harness neighbor writer {writer_index:02d} item {sequence:03d}"


def _neighbor_envelope(session_ref: str, sequence: int, repo: Path, project_id: str,
                       scope_version: int, manifest_digest: str) -> dict:
    """The call envelope one neighbor invocation carries. The client, agent,
    and product are the seeded synthetic ones from the bootstrap step."""
    return {
        'schema_version': '1.0',
        'request_id': f'{session_ref}-{sequence:05d}',
        'client_ref': 'harness-client',
        'principal_ref': '',
        'session_ref': session_ref,
        'agent_ref': 'harness-agent-1',
        'directory': str(repo),
        'worktree': str(repo),
        'ambient_project_id': project_id,
        'selected_product_id': 'harness-product',
        'scope_version': scope_version,
        'manifest_digest': manifest_digest,
    }


def _neighbor_capture_input(writer_index: int, number: int, project_id: str) -> dict:
    """An ordinary capture, shaped exactly like the suite's own step 05."""
    return {
        'title': _neighbor_title(writer_index, number),
        'value_statement': 'The black-box neighbor mode exercises an ordinary concurrent write.',
        'kind': 'task',
        'project_ids': [project_id],
        'priority': 0,
        'urgency': 'standard',
        'workflow_type_ref': 'workflow.implementation',
        'idempotency_key': _neighbor_key(writer_index, number),
    }


def _run_neighbor_worker(config_path: str) -> int:
    """One neighbor process. Reads its JSON config, runs its role loop, and
    records one JSON report line per CLI invocation. A failed invocation is
    recorded and ends the worker; the parent judges the mode from the
    reports, so the worker itself always exits 0 on a clean stop."""
    config = json.loads(Path(config_path).read_text(encoding='utf-8'))
    binary = Path(config['binary'])
    env = config['env']
    repo = Path(config['repo'])
    stop_path = Path(config['stop_path'])
    report_path = Path(config['report_path'])
    role = config['role']
    index = int(config['index'])
    session_ref = config['session_ref']
    project_id = config['project_id']
    scope_version = config['scope_version']
    manifest_digest = config['manifest_digest']
    writes = int(config.get('writes', 0))
    sequence = 0

    def record(report_file, entry: dict) -> None:
        report_file.write(json.dumps(entry, separators=(',', ':')) + '\n')
        report_file.flush()

    def failed(entry: dict, exc: Exception) -> dict:
        entry['detail'] = f"{type(exc).__name__}: {exc}"[:300]
        entry['busy'] = _classify_cli_failure(entry['detail']) == 'busy'
        return entry

    with report_path.open('w', encoding='utf-8') as report_file:
        if role == 'writer':
            for number in range(1, writes + 1):
                sequence += 1
                entry: dict = {'seq': sequence, 'kind': 'write', 'ok': False, 'busy': False}
                try:
                    invoke = _run(binary, ['invoke'], {
                        'call_envelope': _neighbor_envelope(
                            session_ref, sequence, repo, project_id, scope_version, manifest_digest),
                        'tool': 'concord_work_define',
                        'operation': 'capture',
                        'input': _neighbor_capture_input(index, number, project_id),
                    }, env)
                    _assert_envelope_ok(invoke, 'neighbor capture')
                    refs = invoke.get('changed_refs') or []
                    if not refs:
                        raise HarnessError(f"neighbor capture returned no changed_refs: {json.dumps(invoke)[:300]}")
                    entry['ok'] = True
                    entry['work_id'] = refs[0].get('id')
                except Exception as exc:  # noqa: BLE001 — every failure mode is recorded, not raised
                    record(report_file, failed(entry, exc))
                    return 0  # fail fast: any error fails the mode at the parent
                record(report_file, entry)
            return 0

        for _ in range(NEIGHBOR_READER_ITERATION_CAP):
            if stop_path.exists():
                return 0
            sequence += 1
            read_resolve = sequence % 2 == 1
            entry = {
                'seq': sequence,
                'kind': 'read',
                'ok': False,
                'busy': False,
                'op': 'concord_product_view.resolve' if read_resolve else 'concord_work_browse.list',
            }
            try:
                if read_resolve:
                    invoke = _run(binary, ['invoke'], {
                        'call_envelope': _neighbor_envelope(
                            session_ref, sequence, repo, project_id, scope_version, manifest_digest),
                        'tool': 'concord_product_view',
                        'operation': 'resolve',
                        'input': {'project_id': project_id},
                    }, env)
                    _assert_envelope_ok(invoke, entry['op'])
                else:
                    invoke = _run(binary, ['invoke'], {
                        'call_envelope': _neighbor_envelope(
                            session_ref, sequence, repo, project_id, scope_version, manifest_digest),
                        'tool': 'concord_work_browse',
                        'operation': 'list',
                        'input': {'project_ids': [project_id], 'page': {'cursor': '', 'limit': 50}},
                    }, env)
                    _assert_envelope_ok(invoke, entry['op'])
                    if not isinstance((invoke.get('result') or {}).get('items'), list):
                        raise HarnessError(f"{entry['op']} result carries no items array")
                entry['ok'] = True
            except Exception as exc:  # noqa: BLE001 — every failure mode is recorded, not raised
                record(report_file, failed(entry, exc))
                return 0
            record(report_file, entry)
    return 0


def _read_jsonl(path: Path) -> list[dict] | None:
    """Parse a worker report file; None when it is missing or malformed."""
    try:
        lines = path.read_text(encoding='utf-8').splitlines()
    except OSError:
        return None
    entries: list[dict] = []
    for line in lines:
        if not line.strip():
            continue
        try:
            entries.append(json.loads(line))
        except json.JSONDecodeError:
            return None
    return entries


def _aggregate_neighbor_entries(labelled: list[tuple[str, list[dict]]]) -> dict:
    """Aggregate worker reports: busy and error counts, per-worker progress,
    and the writer work identities the final read-back must find."""
    busy = 0
    errors = 0
    reader_ok: dict[str, int] = {}
    writer_ok: dict[str, int] = {}
    expected_ids: list[str] = []
    for label, entries in labelled:
        for entry in entries:
            if not entry.get('ok'):
                if entry.get('busy'):
                    busy += 1
                else:
                    errors += 1
                continue
            if entry.get('kind') == 'read':
                reader_ok[label] = reader_ok.get(label, 0) + 1
            elif entry.get('kind') == 'write':
                writer_ok[label] = writer_ok.get(label, 0) + 1
                expected_ids.append(str(entry.get('work_id')))
    return {
        'busy': busy,
        'errors': errors,
        'reader_ok': reader_ok,
        'writer_ok': writer_ok,
        'expected_ids': expected_ids,
    }


def _neighbor_mode_verdict(aggregate: dict, neighbors: int, writes: int) -> str | None:
    """None when the neighbor mode passed; the failure detail otherwise."""
    if aggregate.get('problems'):
        return '; '.join(aggregate['problems'])
    if aggregate['busy']:
        return f"{aggregate['busy']} busy response(s) under concurrency"
    if aggregate['errors']:
        return f"{aggregate['errors']} error response(s) under concurrency"
    for index in range(neighbors):
        label = f"reader-{index:02d}"
        if aggregate['reader_ok'].get(label, 0) < 1:
            return f"reader {label} completed no successful read"
    for index in range(neighbors):
        label = f"writer-{index:02d}"
        done = aggregate['writer_ok'].get(label, 0)
        if done != writes:
            return f"writer {label} completed {done} of {writes} writes"
    return None


def _reconcile_neighbor_items(expected_ids: list[str], observed_ids: list[str],
                              expected_total: int) -> list[str]:
    """Findings for lost, duplicated, or unexpected work items. An empty list
    is the zero-lost, zero-duplicates verdict."""
    findings: list[str] = []
    observed_counts = Counter(observed_ids)
    expected_counts = Counter(expected_ids)
    for work_id, count in sorted(expected_counts.items()):
        if count > 1:
            findings.append(f"duplicate capture identity {work_id} reported {count} times")
            continue
        seen = observed_counts.get(work_id, 0)
        if seen == 0:
            findings.append(f"lost: {work_id} is absent from the store")
        elif seen > 1:
            findings.append(f"duplicate: {work_id} listed {seen} times")
    if len(observed_ids) != expected_total:
        findings.append(f"work item count {len(observed_ids)} != expected {expected_total}")
    return findings


def _list_project_work_ids(binary: Path, env: dict, repo: Path, context: dict,
                           manifest_digest: str, project_id: str) -> list[str]:
    """Every work id in one project, paged through the real CLI read path."""
    ids: list[str] = []
    cursor = ''
    for _ in range(200):
        invoke = _run(binary, ['invoke'], {
            'call_envelope': _neighbor_envelope(
                'harness-session-1', len(ids) + 1, repo, context['project_id'],
                context['scope_version'], manifest_digest),
            'tool': 'concord_work_browse',
            'operation': 'list',
            'input': {'project_ids': [project_id], 'page': {'cursor': cursor, 'limit': 50}},
        }, env)
        _assert_envelope_ok(invoke, 'neighbor audit concord_work_browse.list')
        result = invoke.get('result') or {}
        items = result.get('items') or []
        ids.extend(str(item.get('id')) for item in items)
        next_cursor = result.get('next_cursor')
        if not next_cursor:
            return ids
        cursor = str(next_cursor)
    raise HarnessError("neighbor audit listing did not reach a final page within 200 pages")


class NeighborMode:
    """Owns the neighbor child processes for one harness run.

    Startup is deterministic: workers spawn only after the store is
    bootstrapped and the project context resolves, and each worker carries a
    JSON config pinned to this run's binary, environment, and context.
    Cleanup is deterministic: the stop file plus a bounded join, and a kill
    plus a recorded problem for any worker that will not stop.
    """

    def __init__(self, binary: Path, env: dict, repo: Path, manifest_digest: str,
                 work: Path, neighbors: int, writes: int) -> None:
        self._binary = binary
        self._env = dict(env)
        self._repo = repo
        self._manifest_digest = manifest_digest
        self._neighbors = neighbors
        self._writes = writes
        self._dir = work / 'neighbor'
        self.stop_path = self._dir / 'STOP'
        self._children: list[tuple[str, subprocess.Popen]] = []
        self._report_paths: list[tuple[str, Path]] = []
        self._started = False
        self._stopped = False
        self._aggregate: dict | None = None

    @property
    def started(self) -> bool:
        return self._started

    @property
    def stopped(self) -> bool:
        return self._stopped

    def start(self, context: dict) -> None:
        """Spawn the readers and writers. Called once, after the context resolves."""
        if self._started:
            raise HarnessError("neighbor mode started twice")
        self._dir.mkdir(parents=True, exist_ok=False)
        self._started = True
        print(f"NEIGHBOR workload readers={self._neighbors} writers={self._neighbors} writes_per_writer={self._writes}", flush=True)
        for index in range(self._neighbors):
            self._spawn_worker(role='reader', index=index,
                               session_ref=f'harness-neighbor-r{index:02d}',
                               writes=0, context=context)
        for index in range(self._neighbors):
            self._spawn_worker(role='writer', index=index,
                               session_ref=f'harness-neighbor-w{index:02d}',
                               writes=self._writes, context=context)

    def _spawn_worker(self, role: str, index: int, session_ref: str, writes: int,
                      context: dict) -> None:
        label = f"{role}-{index:02d}"
        config_path = self._dir / f'config-{label}.json'
        report_path = self._dir / f'report-{label}.jsonl'
        worker_log = self._dir / f'worker-{label}.log'
        config = {
            'binary': str(self._binary),
            'env': self._env,
            'repo': str(self._repo),
            'manifest_digest': self._manifest_digest,
            'role': role,
            'index': index,
            'writes': writes,
            'session_ref': session_ref,
            'project_id': context['project_id'],
            'scope_version': context['scope_version'],
            'stop_path': str(self.stop_path),
            'report_path': str(report_path),
        }
        config_path.write_text(json.dumps(config), encoding='utf-8')
        with worker_log.open('w', encoding='utf-8') as log:
            process = subprocess.Popen(
                [sys.executable, str(Path(__file__).resolve()), '--neighbor-worker', str(config_path)],
                stdout=log, stderr=log, env=os.environ.copy(),
            )
        self._children.append((label, process))
        self._report_paths.append((label, report_path))

    def stop_and_collect(self) -> dict:
        """Stop every worker deterministically and aggregate the reports."""
        if self._stopped and self._aggregate is not None:
            return self._aggregate
        self.stop_path.touch(exist_ok=True)
        problems: list[str] = []
        for label, process in self._children:
            try:
                process.wait(timeout=NEIGHBOR_JOIN_TIMEOUT_SECONDS)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                problems.append(f"neighbor {label} ignored the stop file and was killed")
            if process.returncode != 0:
                problems.append(f"neighbor {label} exited {process.returncode}")
        labelled: list[tuple[str, list[dict]]] = []
        for label, report_path in self._report_paths:
            entries = _read_jsonl(report_path)
            if entries is None:
                problems.append(f"neighbor {label} wrote no readable report")
                entries = []
            labelled.append((label, entries))
        self._stopped = True
        self._aggregate = _aggregate_neighbor_entries(labelled)
        self._aggregate['problems'] = problems
        return self._aggregate


def step_j_neighbor_concurrency(binary: Path, env: dict, repo: Path, context: dict,
                                manifest_digest: str, neighbors: NeighborMode,
                                neighbors_count: int, writes: int) -> None:
    """The neighbors run beside the suite; assert zero busy
    and error responses, real reader progress, and writer effects with no
    lost and no duplicated work items, all through the real CLI."""
    aggregate = neighbors.stop_and_collect()
    for problem in aggregate.get('problems', []):
        print(f"WARN neighbor: {problem}", flush=True)
    verdict = _neighbor_mode_verdict(aggregate, neighbors_count, writes)
    if verdict is not None:
        raise HarnessError(verdict)
    # One capture belongs to the suite itself (step 05); the rest are the
    # neighbors' ordinary writes.
    observed = _list_project_work_ids(binary, env, repo, context, manifest_digest, 'harness-project-1')
    findings = _reconcile_neighbor_items(
        aggregate['expected_ids'], observed, 1 + neighbors_count * writes)
    if findings:
        raise HarnessError('; '.join(findings[:10]))


# ---------------------------------------------------------------------------
# Top-level driver.
# ---------------------------------------------------------------------------


def main() -> int:
    args = _parse_args(sys.argv[1:])
    if args.neighbor_worker:
        return _run_neighbor_worker(args.neighbor_worker)
    if sys.platform != 'linux':
        print(f"SKIP: blackbox harness only runs on Linux; got platform={sys.platform}", flush=True)
        return 0
    if shutil.which('go') is None:
        print("SKIP: go toolchain is not on PATH", flush=True)
        return 0
    if shutil.which('git') is None:
        print("SKIP: git is not on PATH", flush=True)
        return 0
    if not MANIFEST_DIGEST_FILE.is_file():
        print(f"SKIP: manifest digest file missing at {MANIFEST_DIGEST_FILE}", flush=True)
        return 0
    if not ADAPTER_SRC.is_dir():
        print(f"SKIP: adapter directory missing at {ADAPTER_SRC}", flush=True)
        return 0
    manifest_digest = MANIFEST_DIGEST_FILE.read_text(encoding='utf-8').strip()

    work_root = Path(tempfile.mkdtemp(prefix='concord-blackbox-', dir=_preferred_temp_root()))
    work = work_root
    work.mkdir(parents=True, exist_ok=True)
    neighbors: NeighborMode | None = None
    try:
        binary = _build_binary(work)
        _stage_adapter(work)
        env = _isolated_env(work)
        # Create a git base + linked worktree; the harness uses the worktree
        # path in every invocation so mutation capabilities clear CD-0008.
        canonical_repo, repo = _git_init(work)
        # Generate a deterministic Ed25519 seed so nonce behaviour is the only
        # randomness exercised; reruns are reproducible except for the nonce.
        seed = hashlib.sha256(b'harness-deterministic-seed-v1').digest()
        public_key = _publickey(seed)
        if public_key == b'\x00' * 32:
            raise HarnessError("public key derivation produced the neutral element")
        if args.neighbors > 0:
            neighbors = NeighborMode(
                binary=binary, env=env, repo=repo, manifest_digest=manifest_digest,
                work=work, neighbors=args.neighbors, writes=args.neighbor_writes,
            )

        context: dict | None = None
        work_id: str | None = None

        def run_a():
            step_a_version(binary, env)

        def run_b():
            step_b_bootstrap(binary, env, canonical_repo, public_key)

        def run_c():
            nonlocal context
            context = step_c_context(binary, env, repo)
            # Deterministic startup: the neighbors spawn only once the store is
            # bootstrapped and the project context has resolved, so they always
            # find the seeded synthetic product and a valid scope. On any
            # bootstrap failure they never start.
            if neighbors is not None:
                assert context is not None
                neighbors.start(context)

        def run_d():
            assert context is not None
            step_d_read(binary, env, repo, context, manifest_digest)

        def run_e():
            nonlocal work_id
            assert context is not None
            work_id = step_e_mutation(binary, env, repo, context, manifest_digest)

        def run_f():
            assert context is not None
            assert work_id is not None
            step_f_workflow_evidence(binary, env, repo, context, manifest_digest, work_id, 4)

        def run_g():
            assert context is not None
            assert work_id is not None
            step_g_restart_reopen(binary, env, repo, context, manifest_digest, work_id)

        def run_h():
            assert context is not None
            assert work_id is not None
            step_h_backup_restore(binary, env, work, repo, context, manifest_digest, work_id)

        def run_i():
            step_i_upgrade(binary, env, work, repo, seed, public_key)

        def run_j():
            assert neighbors is not None
            assert context is not None
            step_j_neighbor_concurrency(
                binary, env, repo, context, manifest_digest,
                neighbors, args.neighbors, args.neighbor_writes)

        steps = [
            ('01_version_stamp', run_a),
            ('02_bootstrap', run_b),
            ('03_context_resolution', run_c),
            ('04_read_invoke', run_d),
            ('05_mutation_capture', run_e),
            ('06_workflow_evidence', run_f),
            ('07_restart_reopen', run_g),
            ('08_backup_restore', run_h),
            ('09_upgrade_compatibility', run_i),
        ]
        if neighbors is not None:
            # The neighbors run beside steps 04-09; this step stops them and
            # issues the zero-busy/error, progress, and lost/duplicates verdict.
            steps.append(('10_neighbor_concurrency', run_j))
        any_failed = False
        for name, fn in steps:
            if not _step(name, fn):
                any_failed = True
                break  # later steps depend on prior state; stop on first failure
    finally:
        # The suite stops on first failure before the neighbor verdict step;
        # stop the workers anyway so the work tree can be removed, and keep
        # any cleanup problem visible without masking the step failure.
        if neighbors is not None and neighbors.started and not neighbors.stopped:
            try:
                leftover = neighbors.stop_and_collect()
            except Exception as exc:  # noqa: BLE001 — cleanup must never mask the step result
                print(f"WARN neighbor cleanup failed: {type(exc).__name__}: {exc}", flush=True)
            else:
                for problem in leftover.get('problems', []):
                    print(f"WARN neighbor: {problem}", flush=True)
        with contextlib.suppress(Exception):
            shutil.rmtree(work_root)
    return 1 if any_failed else 0


if __name__ == '__main__':
    sys.exit(main())
