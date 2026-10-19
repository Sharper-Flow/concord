"""The complete earlier-release writer population for the release restore gate.

Every nondraft GitHub release is joined to its strict-semver Git tag and its
standalone binary and checksum assets. The join fails closed: an incomplete
page sequence, a duplicate or non-semver release tag, or a published release
not earlier than the candidate refuses the whole population. A release with no
tag or no assets, and a tag with no release, become named `unknown` rows. Only
a tag a recorded ruling settled, bound to its exact tag object and commit, is
`artifact_unavailable`. The candidate's own tag is excluded only when it
already names the target commit (an idempotent release rerun).

The network readers take a `run(argv) -> stdout` callable that raises GateError
on failure, so the caller owns how each `gh` and `git` child is confined,
bounded, and interrupted.
"""

from __future__ import annotations

import dataclasses
import json
import re

TAG_RE = re.compile(r"\Av(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
PER_PAGE = 100
# Strict-semver tags that carry no GitHub release by a recorded ruling. The
# entry binds the tag object and commit, so a retargeted tag is unknown again.
SETTLED_UNPUBLISHED = {
    "v8.28.5": ("e92fd8721495219a9d5a1817e2c8264615325d4d", "7193e6260b142ca86817a0b0128d2898da30ca8c"),
}


class GateError(Exception):
    """The population or candidate cannot be trusted; no writer result is meaningful."""


@dataclasses.dataclass(frozen=True)
class Writer:
    tag: str
    release_id: int
    tag_object: str
    commit: str
    asset_id: int
    asset_size: int
    asset_digest: str
    checksum_asset_id: int
    checksum_size: int


def semver(tag: str) -> tuple[int, int, int]:
    match = TAG_RE.match(tag)
    if not match:
        raise GateError(f"tag {tag!r} is not a strict release version")
    return tuple(int(part) for part in match.groups())  # type: ignore[return-value]


def gh_release_total(repository: str, run) -> int:
    owner, name = repository.split("/")
    query = "query($o:String!,$n:String!){repository(owner:$o,name:$n){releases{totalCount}}}"
    document = json.loads(run(["gh", "api", "graphql", "-f", f"query={query}", "-F", f"o={owner}", "-F", f"n={name}"]))
    return int(document["data"]["repository"]["releases"]["totalCount"])  # type: ignore[index]


def gh_release_page(repository: str, page: int, run) -> list[dict]:
    batch = json.loads(run(["gh", "api", f"repos/{repository}/releases?per_page={PER_PAGE}&page={page}"]))
    if not isinstance(batch, list) or any(not isinstance(entry, dict) for entry in batch):
        raise GateError(f"release page {page} is not a list of objects")
    return batch


def list_releases(total: int, fetch_page) -> list[dict]:
    """Read release pages until the reported total is accounted for exactly."""
    releases: list[dict] = []
    page = 1
    while len(releases) < total:
        batch = fetch_page(page)
        if not batch:
            break
        releases.extend(batch)
        page += 1
    if len(releases) != total:
        raise GateError(f"release pages returned {len(releases)} of {total} releases")
    return releases


def git_tags(repository: str, run) -> dict[str, tuple[str, str]]:
    """Strict-semver tags as {tag: (tag object, peeled commit)} from the public remote."""
    listing = run(["git", "ls-remote", "--tags", f"https://github.com/{repository}.git"])
    objects: dict[str, str] = {}
    peeled: dict[str, str] = {}
    for line in listing.splitlines():
        oid, ref = line.split("\t")
        name = ref.removeprefix("refs/tags/")
        target = peeled if name.endswith("^{}") else objects
        target[name.removesuffix("^{}")] = oid
    return {tag: (oid, peeled.get(tag, oid)) for tag, oid in objects.items() if TAG_RE.match(tag)}


def candidate_rerun(tag: str, tags: dict[str, tuple[str, str]], candidate_version: str, target_sha: str) -> bool:
    """An idempotent release rerun: the candidate's own tag already names the target commit."""
    return tag == candidate_version and tags.get(tag, ("", ""))[1] == target_sha


def enumerate_writers(releases: list[dict], tags: dict[str, tuple[str, str]], candidate_version: str, target_sha: str):
    """Join releases to tags. Returns (writers, unknown rows, unavailable rows, excluded tags)."""
    candidate = semver(candidate_version)
    writers: list[Writer] = []
    unknown: list[dict] = []
    seen: set[str] = set()
    excluded: list[str] = []
    for release in releases:
        if release.get("draft"):
            continue
        tag = release.get("tag_name")
        if not isinstance(tag, str) or tag in seen:
            raise GateError(f"release {release.get('id')} has a missing or duplicate tag {tag!r}")
        seen.add(tag)
        if semver(tag) >= candidate:
            if candidate_rerun(tag, tags, candidate_version, target_sha):
                excluded.append(tag)
                continue
            raise GateError(f"published release {tag} is not earlier than candidate {candidate_version}")
        if tag not in tags:
            unknown.append({"tag": tag, "status": "unknown", "stage": "population", "detail": "release has no tag"})
            continue
        assets = {asset.get("name"): asset for asset in release.get("assets", [])}
        binary, checksum = assets.get(f"concord-{tag}"), assets.get(f"concord-{tag}.sha256")
        if binary is None or checksum is None:
            unknown.append({"tag": tag, "status": "unknown", "stage": "artifact", "detail": "binary or checksum asset is absent"})
            continue
        writers.append(Writer(tag, int(release["id"]), tags[tag][0], tags[tag][1], int(binary["id"]), int(binary["size"]),
                              str(binary.get("digest") or ""), int(checksum["id"]), int(checksum["size"])))
    unavailable: list[dict] = []
    for tag in sorted(set(tags) - seen, key=semver):
        if candidate_rerun(tag, tags, candidate_version, target_sha):
            excluded.append(tag)
        elif SETTLED_UNPUBLISHED.get(tag) == tags[tag]:
            unavailable.append({"tag": tag, "status": "artifact_unavailable", "tag_object": tags[tag][0], "commit": tags[tag][1]})
        else:
            unknown.append({"tag": tag, "status": "unknown", "stage": "population", "detail": "tag has no published release"})
    writers.sort(key=lambda writer: semver(writer.tag))
    return writers, unknown, unavailable, excluded
