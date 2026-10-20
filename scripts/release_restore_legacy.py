#!/usr/bin/env python3
"""The source proof that alone admits the bootstrap-only legacy restore variant.

The early writers v0.1.0-v0.7.1 construct capture results with a string
ChangedRef.Version (internal/agent/envelope.go) and marshal them directly
(mutationPayload in internal/agent/mutations.go), while the embedded schema
binds changed_refs[].version to an integer >= 1
(internal/agent/generated_payload_schemas.go, enforced by the validator in
internal/agent/payload_validation.go called from Envelope.MarshalJSON).
Their capture output therefore cannot serialize; their operator pair
(cmd/concord/main.go runInternal/writeOperatorResult over
internal/store/operator.go and project_locator.go) never touches
agent.Envelope. Released operator behavior still needs execution proof.
v0.8.0 corrects capture with integer
result refs, so only the pinned pre-correction tuples are legacy candidates.

A legacy writer therefore admits a bootstrap-only restore recipe: the
product-create and project-locator-add pair, with no client registration,
grant, or invoke. Because that variant runs no invoke, it also loses the
manifest-digest check that otherwise catches source mismatch, so admission
requires the complete whole-file proof, never a tag name, version cutoff, or
symbol-presence test:

- every owner file below is hashed as raw Git blob content at the tag's
  peeled commit, and the canonical mapping of owner path -> blob SHA-256 is
  hashed into the source fingerprint;
- the writer's remote release commit and the local checkout's commit must
  both bind the pinned commit, so a re-tagged checkout or a re-pointed
  release holds by name;
- the computed owner bytes must equal the pinned bytes, so a drifted,
  disconnected, or locally shadowed owner (git replace, CRLF, corruption)
  holds by name naming the owner.

Identical owner tuples are deduplicated: v0.1.0/v0.2.0, v0.3.0-v0.5.0, and
v0.6.0-v0.7.1 each share one tuple, and v0.8.0 differs from v0.7.1 in
mutations.go alone. Any tag without a pin row selects nothing here and
continues through the existing capture/message machinery, whose invoke
manifest digest still binds its source. Unknown input never becomes a
successful skip.
"""

from __future__ import annotations

import hashlib
import json
import subprocess
from pathlib import Path

LEGACY_VARIANT = "legacy-bootstrap"
CORRECTED_VARIANT = "corrected"

# Every custom source owner the proof trusts, in canonical order: the capture
# result value flow and its direct marshaling (mutations.go), the changed-ref
# type and the envelope marshal/validation chain (envelope.go), the
# capture-to-schema selection and the schema validator implementation
# (payload_validation.go), the generated embedded schema document and its
# integer version definition (generated_payload_schemas.go), the generated
# payload rules plus the generated registration surface
# (generated_contracts.go), the operator command dispatch and output path
# (cmd/concord/main.go), and the bootstrap store writers (operator.go,
# project_locator.go). Standard-library semantics are not owners.
LEGACY_OWNERS = (
    "internal/agent/mutations.go",
    "internal/agent/envelope.go",
    "internal/agent/payload_validation.go",
    "internal/agent/generated_payload_schemas.go",
    "internal/agent/generated_contracts.go",
    "cmd/concord/main.go",
    "internal/store/operator.go",
    "internal/store/project_locator.go",
)

# Whole-file tuples must match both the reviewed source and its tagged Git bytes.
_V0_1 = {
    "internal/agent/mutations.go": "29a767e29ef39026c2bb9d99399cc3bf95759fa3e336a093a1faeb13fbed2845",
    "internal/agent/envelope.go": "738c06327692521f3c458290b8e766f2ad2c76e57815c6111cbcf1dec63ec317",
    "internal/agent/payload_validation.go": "75364d9509b5732d333fe3d77b9bff0401ff59d94b00ff29801bbc0fd87af261",
    "internal/agent/generated_payload_schemas.go": "c9d720f81be101531987265fa6e8d99ce5180f1d51c7358d7545db71a99f3c28",
    "internal/agent/generated_contracts.go": "05c437f044ed6debe878248d9c72bfe9490e24a9439739de243d891f0ee68f17",
    "cmd/concord/main.go": "11e7457eb4baf5cbd87f0ac165b7f1bdad6c6489cc7c8b69798b386857392124",
    "internal/store/operator.go": "60e647a6304a46430a7797dae631413d1eb8c2bd30080f330056f382ce3c41e1",
    "internal/store/project_locator.go": "4c462100e1fdad7f87934866165eeba8f2e62b2bcd62a5ff2c8a6e3054bcc599",
}
_V0_3 = {
    "internal/agent/mutations.go": "44c69f04b914794931c9f0f3ab801a1038bae6a3e7eb7be6539828a0b0e7d03f",
    "internal/agent/envelope.go": "7ec673fc9c9250a3afb90daa8ccb9fa40d7a9eda789fd1c1c00861b17eab080c",
    "internal/agent/payload_validation.go": "282236e4252fa27dd26df7a3fed15039c801f935e174fb1a132bc53e6b2fbbaf",
    "internal/agent/generated_payload_schemas.go": "fbaf13106e2125d668fd1c3f226e7b65d753fa7313930f765bcaed512097654c",
    "internal/agent/generated_contracts.go": "e2bfb3316a575a06288e82cc1648a0b546d790ab8a5499884d97b9e878bc3323",
    "cmd/concord/main.go": "e8d069f948bd29215955a45b2580829cd24bf3639c44886f9bc3693cddbafec6",
    "internal/store/operator.go": "60e647a6304a46430a7797dae631413d1eb8c2bd30080f330056f382ce3c41e1",
    "internal/store/project_locator.go": "4c462100e1fdad7f87934866165eeba8f2e62b2bcd62a5ff2c8a6e3054bcc599",
}
_V0_6 = {
    "internal/agent/mutations.go": "44c69f04b914794931c9f0f3ab801a1038bae6a3e7eb7be6539828a0b0e7d03f",
    "internal/agent/envelope.go": "7ec673fc9c9250a3afb90daa8ccb9fa40d7a9eda789fd1c1c00861b17eab080c",
    "internal/agent/payload_validation.go": "aec8da850fab0f26f4f4d06473ea8c290cc9b1218acefb8c2cdf041f147ca6b7",
    "internal/agent/generated_payload_schemas.go": "7cfdcef6e6c659da96230a42cec29a462d1fb17c0202ea3e8df8f81939c55b88",
    "internal/agent/generated_contracts.go": "60ec8c20a43b77f1a92ccfc5f501c70102235f22d3b346131ae170c72b14e47b",
    "cmd/concord/main.go": "6bbf5261e5e5e69d132c8736cda42e02f925029751c9da16ab2683c43b0dbe96",
    "internal/store/operator.go": "aad1d2c49fbbbdcc178910ed6d50f699e5787d060417d7fc60cfa1366be96966",
    "internal/store/project_locator.go": "4c462100e1fdad7f87934866165eeba8f2e62b2bcd62a5ff2c8a6e3054bcc599",
}
# v0.8.0 (93cd5cb) corrects the capture result refs: its tuple equals
# v0.7.1's everywhere except mutations.go, which is the proof boundary.
_V0_8 = {**_V0_6, "internal/agent/mutations.go": "1cdb598151f4d83c4d5ef1897f45041d419179105686ad2b278cc7aa2fa46110"}

# One row per released tag: the remote-peeled and local-resolved commit the
# variant binds, the selected variant, and the owner tuple it pins.
LEGACY_SOURCE_PINS = (
    {"tag": "v0.1.0", "commit": "897fcda9f9dc4a7be31a2e655b1d80995c4e9072", "variant": LEGACY_VARIANT, "owners": _V0_1},
    {"tag": "v0.2.0", "commit": "d0f0ba1fdc67b2cc0bd93bbc73b72a33e3f5aced", "variant": LEGACY_VARIANT, "owners": _V0_1},
    {"tag": "v0.3.0", "commit": "5b6a0d1e55ae440d2a095c4bdc045a7d8984e4f7", "variant": LEGACY_VARIANT, "owners": _V0_3},
    {"tag": "v0.4.0", "commit": "e3655a7a1ad94a5aaa0b7e2e47f7f6a88d65b643", "variant": LEGACY_VARIANT, "owners": _V0_3},
    {"tag": "v0.5.0", "commit": "9b85248442e919906cdeb81e5f1f11a43b6f4d7c", "variant": LEGACY_VARIANT, "owners": _V0_3},
    {"tag": "v0.6.0", "commit": "e092e81789611c4636d5325d72113ef6c9b0cceb", "variant": LEGACY_VARIANT, "owners": _V0_6},
    {"tag": "v0.7.0", "commit": "a13793d3b1d7d1667ec2e8d808ae2b119fc746d9", "variant": LEGACY_VARIANT, "owners": _V0_6},
    {"tag": "v0.7.1", "commit": "3605af1da8367e490fe3cea3bbc8ffa3a90b5964", "variant": LEGACY_VARIANT, "owners": _V0_6},
    {"tag": "v0.8.0", "commit": "93cd5cbd97ba82cbd81bf091c4d577095da918bb", "variant": CORRECTED_VARIANT, "owners": _V0_8},
)


class LegacyProofError(Exception):
    """A named legacy-proof failure: (stage, detail)."""

    def __init__(self, stage: str, detail: str):
        super().__init__(f"{stage}: {detail}")
        self.stage, self.detail = stage, detail


def fingerprint_of(owners: dict[str, str]) -> str:
    """The recognized source fingerprint: the canonical owner-path -> blob-hash mapping, hashed."""
    canonical = json.dumps(owners, sort_keys=True, separators=(",", ":"))
    return "sha256:" + hashlib.sha256(canonical.encode()).hexdigest()


def _git_bytes(repo: Path, *argv: str) -> bytes:
    proc = subprocess.run(["git", "-C", str(repo), *argv], capture_output=True, timeout=120)
    if proc.returncode != 0:
        raise LegacyProofError("legacy-proof", f"git {' '.join(argv)} exited {proc.returncode}: {proc.stderr.decode(errors='replace').strip()[-200:]}")
    return proc.stdout


def tag_commit(repo: Path, tag: str) -> str:
    """The tag's peeled commit in this checkout."""
    return _git_bytes(repo, "rev-parse", f"{tag}^{{commit}}").decode().strip()


def source_fingerprint(repo: Path, commit: str) -> dict:
    """The whole-file owner mapping at one commit and its canonical fingerprint."""
    owners = {path: hashlib.sha256(_git_bytes(repo, "show", f"{commit}:{path}")).hexdigest() for path in LEGACY_OWNERS}
    return {"commit": commit, "fingerprint": fingerprint_of(owners), "owners": owners}


def select_variant(repo: Path, tag: str, remote_commit: str | None) -> tuple[str, dict] | None:
    """The variant the source proof selects for one writer, or None for the existing machinery.

    A tag with a pin row must bind its pinned commit from both sides - the
    release's remote commit and the checkout's own tag - and its locally read
    owner bytes must equal the pinned bytes. Any disagreement is a named hold:
    a drifted or shadowed source never becomes bootstrap-only.
    """
    rows = [row for row in LEGACY_SOURCE_PINS if row["tag"] == tag]
    if not rows:
        return None
    row = rows[0]
    if remote_commit != row["commit"]:
        raise LegacyProofError("legacy-proof", f"{tag} release commit {remote_commit or 'absent'} does not bind the pinned source commit {row['commit']}")
    local_commit = tag_commit(repo, tag)
    if local_commit != row["commit"]:
        raise LegacyProofError("legacy-proof", f"{tag} checkout commit {local_commit} does not bind the pinned source commit {row['commit']}")
    proof = source_fingerprint(repo, local_commit)
    if proof["owners"] != row["owners"]:
        drifted = [path for path in LEGACY_OWNERS if proof["owners"].get(path) != row["owners"][path]]
        raise LegacyProofError("legacy-proof", f"{tag} source drifted from the pinned owner bytes: {', '.join(drifted)}")
    return row["variant"], proof
