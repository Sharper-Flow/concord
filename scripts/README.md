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

`install.py` is the standard-library-only Concord release installer. It verifies
published checksums before changing managed data, adapter, launcher, or
OpenCode skill-path files. It journals phase boundaries and recovers interrupted
install, upgrade, and uninstall operations; `status` triggers recovery without
changing the requested installation state. `test-release.py` and
`test-installer.py` exercise release and installation behavior entirely in
temporary repositories/roots.
