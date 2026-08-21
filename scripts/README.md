# Repository validators

The scripts in this directory are dependency-free checks for Markdown links,
public-content boundaries, and tracked JSON. CI runs them before Go checks.

`check-commit-title.py` guards the subject line that reaches `main`. The
repository squashes with `squash_merge_commit_title=PR_TITLE`, so a
pull-request title becomes a commit subject verbatim and `release.py` derives
the next version from it. The checker imports `release.py`'s Conventional
Commit grammar rather than restating it, so what CI accepts and what the
release reads cannot drift, and closes the type vocabulary that `release.py`
deliberately leaves open. `.github/workflows/pr-title.yml` runs it.

`check-law-coverage.py` and `check-reachability.py` implement CD-0047. The
first gives every knowledge-index record a coverage state and resolves the typed
anchors a `satisfied` record cites, deriving its subject set from the index so a
record cannot escape by being absent. The second runs a version-pinned
`deadcode` over `./cmd/...`, subtracts `docs/reachability-exceptions.v1.json`,
and fails on any remainder or on a declaration that has become reachable. They
share one state vocabulary through `coverage_state.py`, which is a single
definition rather than two copies kept equal by a test.

`install.py` is the standard-library-only Concord release installer. It verifies
published checksums before changing managed data, adapter, launcher, or
OpenCode skill-path files. It journals phase boundaries and recovers interrupted
install, upgrade, and uninstall operations; `status` triggers recovery without
changing the requested installation state. `test-release.py` and
`test-installer.py` exercise release and installation behavior entirely in
temporary repositories/roots.
