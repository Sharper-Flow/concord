#!/usr/bin/env python3
"""Loader for the closed legislated-by-default cohort asset.

The committed asset internal/store/knowledge_authority_defaults.v1.json is the
only list of the frozen cohort: the records whose legislated standing the
application snapshot (CD-0159 D5) fixed through the named vehicle contract at
the named version. scripts/check-knowledge-index.py and
scripts/generate-knowledge-index.py share this loader so the cohort is never
duplicated into a second list; the Go store embeds the same bytes.

A vehicle claim (legislated_by equal to the asset's vehicle work id) must name
a frozen (record_id, kind) member at exactly the asset's contract version. Any
other declared reference only needs the work- id grammar; its approval is
verified at runtime by the store resolver, never here.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

# The asset lives under the repository root that contains this script's
# scripts/ directory. Loading through this module-owned root (not a caller's
# working root) keeps checker, generator, and tests on the one committed file.
REPO_ROOT = Path(__file__).resolve().parents[1]
ASSET_RELPATH = Path("internal/store/knowledge_authority_defaults.v1.json")
ASSET_SCHEMA = "knowledge-authority-defaults.v1"

# The declared legislative reference grammar, mirroring
# contracts/concord-knowledge-index.v1.schema.json ($defs.authority and
# $defs.criterionBinding) and the store parser's work- id pattern.
WORK_ID_PATTERN = re.compile(r"^work-[0-9a-f]{8,64}$")

_ALLOWED_MEMBER_KEYS = {"record_id", "kind"}
_ALLOWED_ROOT_KEYS = {"schema", "application_sha", "vehicle", "members"}
_SHA_PATTERN = re.compile(r"^[0-9a-f]{40,64}$")
_MAX_ID = 256


class AuthorityDefaultsError(Exception):
    """The committed cohort asset is missing, malformed, or inconsistent."""


class AuthorityDefaults:
    """The parsed closed cohort: one vehicle and its frozen members."""

    def __init__(self, document: object) -> None:
        if not isinstance(document, dict) or set(document) != _ALLOWED_ROOT_KEYS:
            raise AuthorityDefaultsError("asset root is not the closed v1 object")
        if document["schema"] != ASSET_SCHEMA:
            raise AuthorityDefaultsError("asset schema is not " + ASSET_SCHEMA)
        application_sha = document["application_sha"]
        if not isinstance(application_sha, str) or not _SHA_PATTERN.fullmatch(application_sha):
            raise AuthorityDefaultsError("asset application_sha is not a git object name")
        vehicle = document["vehicle"]
        if not isinstance(vehicle, dict) or set(vehicle) != {"work_id", "contract_version"}:
            raise AuthorityDefaultsError("asset vehicle is not the closed two-field object")
        if not isinstance(vehicle["work_id"], str) or not WORK_ID_PATTERN.fullmatch(vehicle["work_id"]):
            raise AuthorityDefaultsError("asset vehicle work_id is not a work- prefixed id")
        version = vehicle["contract_version"]
        if not isinstance(version, int) or isinstance(version, bool) or version < 1:
            raise AuthorityDefaultsError("asset vehicle contract_version is not a positive integer")
        members = document["members"]
        if not isinstance(members, list) or not members:
            raise AuthorityDefaultsError("asset members is not a non-empty array")
        parsed: list[tuple[str, str]] = []
        for member in members:
            if not isinstance(member, dict) or set(member) != _ALLOWED_MEMBER_KEYS:
                raise AuthorityDefaultsError("asset member is not the closed two-field object")
            record_id, kind = member["record_id"], member["kind"]
            for value in (record_id, kind):
                if not isinstance(value, str) or not 0 < len(value) <= _MAX_ID or value != value.strip():
                    raise AuthorityDefaultsError("asset member field is empty, oversized, or not clean")
            parsed.append((record_id, kind))
        if parsed != sorted(parsed):
            raise AuthorityDefaultsError("asset members are not sorted by (record_id, kind)")
        if len(set(parsed)) != len(parsed):
            raise AuthorityDefaultsError("asset members carry a duplicate (record_id, kind)")
        self.application_sha: str = application_sha
        self.vehicle_work_id: str = vehicle["work_id"]
        self.vehicle_contract_version: int = version
        self.members: frozenset[tuple[str, str]] = frozenset(parsed)

    def vehicle_membership_error(self, record_id: object, kind: object, contract_version: object) -> str | None:
        """None when the declared claim names the frozen cohort exactly.

        A vehicle claim requires the exact frozen (record_id, kind) member and
        exactly the asset's contract version; anything else is refused. The
        declared tier is checked by the caller, which only reaches here for a
        legislated record.
        """
        if contract_version == self.vehicle_contract_version and (record_id, kind) in self.members:
            return None
        if (record_id, kind) in self.members:
            return (
                f"vehicle claim for {record_id} must carry contract_version "
                f"{self.vehicle_contract_version}"
            )
        return f"vehicle claim for {record_id} ({kind}) is outside the frozen legislated-by-default cohort"


def load(root: Path | None = None) -> AuthorityDefaults:
    """Parse the committed asset; every failure raises, never skips."""
    path = (root or REPO_ROOT) / ASSET_RELPATH
    try:
        raw = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise AuthorityDefaultsError(f"cannot read {ASSET_RELPATH}: {exc}") from exc
    try:
        document = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise AuthorityDefaultsError(f"invalid JSON in {ASSET_RELPATH}: {exc}") from exc
    return AuthorityDefaults(document)
