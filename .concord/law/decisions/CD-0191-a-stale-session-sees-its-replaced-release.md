---
{
  "id": "CD-0191",
  "title": "A stale session sees its replaced release",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-fd94329981b8575144fe93ed",
    "contract_version": 3
  },
  "home_domain_id": "agent-adapter",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0111"
    }
  ]
}
---
# CD-0191: A stale session sees its replaced release

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** Installed-versus-session release notices and retained-release holders
- **Related:** CD-0111, CD-0132, CD-0242

## Context

CD-0111 pins a session's core and adapter pair. Installation can repoint the
current release while an existing session continues using that pair.

## Decision

### D1. The adapter owns installed-versus-session staleness

At each Concord tool call, the adapter reads `current` beside its stamped
release root. A different installed release is stale in either direction,
including rollback. An absent or unreadable link is no observed mismatch;
staleness is not guessed from absence.

### D2. A stale result carries one bounded notice

Each stale result names the pinned and installed versions and the reload
remedy in the warnings channel. If warnings are full, the notice uses the
tool-result output layer. Existing calls keep working under their pinned pair.
The notice supplies no workflow authority and imposes no subagent launch gate.

### D4. Installation names retained-release holders

Retention output enumerates sessions holding each retained release, with
process identity and directory or worktree. The operator can identify which
sessions need reload. Lease-held release retention remains under CD-0111.

## Alternatives considered

- Refuse every stale call. Rejected: it would break running sessions on install.
- Guess staleness from version ordering. Rejected: rollback also changes the pair.
- Add another envelope field. Rejected: bounded notices already have an owner.

## Consequences

A running session keeps its pinned pair and sees the observed release mismatch.
Concord creates no native Task admission or worker-dispatch refusal.

## Verification

- `bun test adapter/opencode/` checks release comparison and bounded notices.
- `adapter/opencode/concord.test.ts` retains the installed-release resolver and
  bounded per-result staleness-notice checks without a worker dispatch gate.
- `python3 scripts/test-installer.py` checks retained-release holder output.
