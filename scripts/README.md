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

`check-knowledge-vocabulary.py` makes
`contracts/concord-knowledge-index.v1.schema.json` the one declaration of the
knowledge manifest vocabulary. CI carries no JSON Schema validator, so
`check-knowledge-index.py` restates that vocabulary as Python sets and
`internal/store` restates it again as Go maps. This checker fails when the
schema and the Python sets disagree in either direction;
`TestKnowledgeManifestVocabularyMatchesSchema` does the same for Go. Adding a
record kind or field is therefore a schema edit plus two failing checks that
name exactly what to update.

`check-knowledge-closure.py` reports three separate numbers, and the split is
the point. Unprocessed documents are source material still awaiting a decision.
Exclusions are paths that are not knowledge at all, either a whole directory or
one generated file. Dispositions are documents the operator has decided will
never be formalized, and they are counted and listed on their own line even when
the count is zero. Folding a disposition into the unprocessed count would hide a
growing pile of refusals behind a shrinking backlog; folding it into an exclusion
would hide it entirely.

`install.py` is the standard-library-only Concord release installer. It verifies
published checksums before changing managed data, adapter, launcher, or
OpenCode skill-path files. It journals phase boundaries and recovers interrupted
install, upgrade, and uninstall operations. It also verifies or initializes the
user Secret Service after artifact verification and before activation. `status` triggers recovery
without changing the requested installation state. `test-release.py` and
`test-installer.py` exercise release and installation behavior entirely in
temporary repositories/roots.
