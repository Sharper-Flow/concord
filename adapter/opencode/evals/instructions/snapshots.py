"""Freeze and verify private instruction snapshots outside the repository."""

import base64
import hashlib
import json
import os
from pathlib import Path


_KINDS = ("role", "instruction", "agents", "host", "catalog")


def outside_repository(path, repository):
    """Resolve a path and reject the repository and its descendants."""
    path = Path(path).resolve()
    repository = Path(repository).resolve()
    if path.is_relative_to(repository):
        raise ValueError("private paths must be outside the repository")
    return path


def _digest(data):
    return hashlib.sha256(data).hexdigest()


def _canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def _body(text):
    lines = text.splitlines(keepends=True)
    if lines and lines[0].strip() == "---":
        for index in range(1, len(lines)):
            if lines[index].strip() in ("---", "..."):
                return "".join(lines[index + 1:])
        raise ValueError("unterminated role frontmatter")
    return text


def freeze(destination, repository, *, role_file, instruction_files, concord_ref,
           host_ref, agents_file=None, host_files=(), catalog_file=None):
    """Create a write-once snapshot with original bytes and normalized role body.

    Sources may be tracked repository files; only the snapshot must lie outside
    the repository. References describe the exact supplied sources; the caller
    establishes their accuracy. Files are stored in the private snapshot and
    must not be secrets.
    """
    destination = outside_repository(destination, repository)
    if not concord_ref or not concord_ref.strip() or not host_ref or not host_ref.strip():
        raise ValueError("Concord and host references are required")
    sources = [("role", role_file)]
    sources += [("instruction", path) for path in instruction_files]
    if agents_file is not None:
        sources.append(("agents", agents_file))
    sources += [("host", path) for path in host_files]
    if catalog_file is not None:
        sources.append(("catalog", catalog_file))
    entries = []
    for kind, path in sources:
        source = Path(path).resolve()
        data = source.read_bytes()
        text = data.decode("utf-8")
        entries.append({"kind": kind, "source": str(source), "sha256": _digest(data),
                        "bytes": base64.b64encode(data).decode("ascii"), "text": text})
    role = _body(entries[0]["text"])
    payload = {"version": 1, "role": role,
               "instructions": [entry["text"] for entry in entries if entry["kind"] in ("instruction", "agents", "host")],
               "catalog": next((entry["text"] for entry in entries if entry["kind"] == "catalog"), ""),
               "provenance": {"concord_ref": concord_ref, "host_ref": host_ref},
               "role_body_sha256": _digest(role.encode("utf-8")), "sources": entries}
    payload["snapshot_id"] = _digest(_canonical(payload))
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    fd = os.open(destination, flags, 0o600)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(_canonical(payload) + b"\n")
        os.chmod(destination, 0o400)
    except BaseException:
        destination.unlink(missing_ok=True)
        raise
    return payload["snapshot_id"]


def load(path, repository):
    """Load a snapshot only when its content and all source hashes verify."""
    path = outside_repository(path, repository)
    snapshot = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(snapshot, dict) or snapshot.get("version") != 1:
        raise ValueError("unsupported snapshot version")
    identity = snapshot.get("snapshot_id")
    content = dict(snapshot)
    content.pop("snapshot_id", None)
    if not isinstance(identity, str) or identity != _digest(_canonical(content)):
        raise ValueError("snapshot identity mismatch")
    try:
        entries = snapshot["sources"]
        kinds = [entry["kind"] for entry in entries]
        if not kinds or kinds[0] != "role" or kinds.count("role") != 1 or any(k not in _KINDS for k in kinds):
            raise ValueError("snapshot source kinds invalid")
        for entry in entries:
            data = base64.b64decode(entry["bytes"], validate=True)
            if _digest(data) != entry["sha256"] or data.decode("utf-8") != entry["text"]:
                raise ValueError("snapshot source mismatch")
        role = _body(entries[0]["text"])
        if (snapshot["role"] != role
                or snapshot["role_body_sha256"] != _digest(role.encode("utf-8"))
                or snapshot["instructions"] != [e["text"] for e in entries if e["kind"] in ("instruction", "agents", "host")]
                or snapshot["catalog"] != next((e["text"] for e in entries if e["kind"] == "catalog"), "")):
            raise ValueError("snapshot projection mismatch")
        if not all(isinstance(snapshot["provenance"].get(key), str) and snapshot["provenance"][key].strip()
                   for key in ("concord_ref", "host_ref")):
            raise ValueError("snapshot references missing")
    except (KeyError, TypeError, AttributeError) as exc:
        raise ValueError("malformed snapshot") from exc
    return snapshot
