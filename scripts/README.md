# Repository validators

The scripts in this directory are dependency-free checks for Markdown links,
public-content boundaries, and tracked JSON. CI runs them before Go checks.

`install.py` is the standard-library-only Concord release installer. It verifies
published checksums before changing managed data, adapter, launcher, or
OpenCode skill-path files. It journals phase boundaries and recovers interrupted
install, upgrade, and uninstall operations; `status` triggers recovery without
changing the requested installation state. `test-release.py` and
`test-installer.py` exercise release and installation behavior entirely in
temporary repositories/roots.
