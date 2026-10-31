# CD-0191: Installation names retained-release holders

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** Installer output for retained releases
- **Related:** [CD-0111](CD-0111-a-release-never-requires-a-session-restart.md)

## Decision

### D4. Installation names retained-release holders

Retention output enumerates sessions holding each retained release, with process
identity and directory or worktree. The operator can identify the holders.
Lease-held release retention, exact adapter/core pairing, and migration safety
remain under CD-0111. Session lifecycle belongs to the host.

## Verification

- `python3 scripts/test-installer.py` checks retained-release holder output.
