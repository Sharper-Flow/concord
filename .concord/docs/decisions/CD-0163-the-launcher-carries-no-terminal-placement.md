# CD-0163: Concord carries no terminal placement

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The terminal-placement boundary for every Concord binary and
  command
- **Approval:** The operator approved the launcher objective that surfaced
  the conflict and this correcting record.
- **Related:** CD-0078, CD-0108, CD-0014, CD-0219, C18
- **Amended:** CD-0219 (2026-10-09) retired the interactive launcher TUI and
  re-homed the boundary to the session entry route. Concord (CON) issue CON-910 (2026-10-13)
  removes the session entry route under the Snowball coordinator's
  operator-delegated approval. The boundary now binds every Concord binary
  and command with no entry route at all.
- **Preserves:** The CD-0078 D1 placement boundary and the store-write-free
  managed-entry boundary of CD-0108 D4

## Context

CD-0078 D1, accepted 2026-08-26, forbids every Concord component from
knowing a terminal multiplexer. It keeps placement with the host.

CD-0108 D1, accepted 2026-09-03, worded the remade launch action as opening
a terminal tab on the work's Concord worktree. The remake record
reintroduced the placement the earlier boundary had removed, so the two
texts could not both govern the launcher. This record repaired the text so
the accepted decision stated what the accepted code did.

The repair outlives the surface it repaired. The entry route is removed,
and the boundary this record states now binds every Concord
binary and command with no session entry route at all.

## Decision

### D1. Concord starts no session, and the operator owns the terminal

No Concord component creates, names, splits, focuses, or destroys a tab,
pane, window, or session, exactly as CD-0078 D1 states. The host starts and
resumes operator sessions and places their surfaces. A host-started session
enters managed work through `concord_work_start`, which moves the calling
session and creates no surface (CD-0098).

### D2. Retired: resume and prompt pass-through

Resume and prompt pass-through were entry-route duties and retired with the
route. Resumption of workflow position is the CD-0016 canonical projection,
delivered by work-start preparation and re-asserted by the per-turn hook
(CD-0031, CD-0090), never a terminal session identifier.

## Consequences

- The placement boundary binds with no session entry route; there is no
  launch action left to place a surface.
- The command-source boundary test that scans for multiplexer names stays
  the structural proof of D1.
- A future route that reattaches a live session still places nothing.
  Reattachment targets a session the host already placed.

## Verification

- `go test ./cmd/concord/ -run TestLauncherAndCommandCarryNoMultiplexerKnowledge`
  proves D1 structurally through the command-source scan.
- `bun test adapter/opencode/concord.test.ts -t "a successful work start
  forks no terminal command"` proves the work-start path forks no terminal
  command.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.
