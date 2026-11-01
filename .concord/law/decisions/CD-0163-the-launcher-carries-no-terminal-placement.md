---
{
  "id": "CD-0163",
  "title": "Concord carries no terminal placement",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "operator-surface",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0078"
    },
    {
      "kind": "refines",
      "target_id": "CD-0108"
    }
  ]
}
---
# CD-0163: Concord carries no terminal placement

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The terminal-placement boundary for every Concord binary and
  command
- **Approval:** The operator approved the launcher objective that surfaced
  the conflict and this correcting record. The Snowball coordinator approved
  session entry route removal for Concord (CON) issue CON-910 on 2026-10-13
  under the operator's delegation.
- **Related:** CD-0078, CD-0108, CD-0014, CD-0219, C18
- **Preserves:** The CD-0078 D1 placement boundary and the store-write-free
  managed-entry boundary of CD-0108 D4

## Context

CD-0078 D1, accepted 2026-08-26, forbids every Concord component from
knowing a terminal multiplexer. It keeps placement with the host.

The terminal-placement boundary binds every Concord binary and command.

## Decision

### D1. Concord starts no session, and the operator owns the terminal

No Concord component creates, names, splits, focuses, or destroys a tab,
pane, window, or session, exactly as CD-0078 D1 states. The host starts and
resumes operator sessions and places their surfaces. Host tools own worktree
placement too (CD-0088 D1).

### D2. Workflow position comes from explicit continuity

Resumption of workflow position is the CD-0016 canonical projection,
delivered by session preparation and explicit Concord reads (CD-0031), never
a terminal session identifier.

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
- `python3 scripts/check-json.py` and `python3 scripts/check-doc-links.py`
  pass.
