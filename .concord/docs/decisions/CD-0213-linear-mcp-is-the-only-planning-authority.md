# CD-0213: Linear MCP is the only planning authority

- **Status:** Accepted
- **Date:** 2026-10-09
- **Scope:** Where planning facts live, how agents read and write them, which
  Linear facts Concord keeps, and how pull requests link to Linear,
  [Concord (CON) issue 801](https://linear.app/sharper-flow/issue/CON-801)
- **Supersedes:** CD-0121, CD-0155, CD-0167, CD-0171, CD-0188
- **Amends:** CD-0041 D8 and D9 items 4-5 as a compatible amendment in place
- **Preserves:** CD-0122 in full, CD-0156 in full, CD-0142 removal handoff,
  CD-0146 external-reference collision refusal, repository review and merge
  evidence, and branch and worktree isolation
- **Related:** CD-0036, CD-0041, CD-0122, CD-0142, CD-0146, CD-0156
- **Approval:** The operator approved this route on 2026-10-09 under CON-801.
  The decision cancels CON-35 and CON-501 and rejects repair of the Concord
  planning mirror.

## Context

Concord keeps a second copy of planning state. It records a planning mode for
each Product, stores Initiatives with ordered entries and narratives, maps each
Initiative to a Linear Project, and queues issue, comment, and Project writes in
a local outbox that a drain sends to the Linear API. The binary holds a Linear
credential for that drain and for a resume-time remote read.

Each surface duplicates a fact that Linear already owns. Each sync route adds
a failure mode: stranded outbox rows, title limits, membership comments, a
main-checkout restriction, and divergence reports between the two copies.
Agents already reach Linear directly through the Linear MCP server, so the
mirror adds cost and no authority.

## Decision

### D1. Linear is the only planning authority

Planning facts live in Linear only. Planning facts are issues, Projects,
Initiatives, priority, workflow state, labels, comments, and the backlog.
Agents read and write them only through the Linear MCP server. Concord keeps no
planning copy, no planning mode, no outbox, and no Project or Initiative
mapping.

### D2. Concord keeps managed execution state

Concord keeps the facts of explicitly managed execution. These facts are the
managed work unit, its typed execution contract, sessions, worktrees, worker
dispatch, verification evidence, and delivery receipts. Workflow transitions,
verdicts, and completion stay Concord authority.

### D3. One Linear issue identity per managed work unit

Each managed work unit carries at most one Linear issue identity: the issue
key, the issue UUID, and the issue URL. The agent creates or finds the issue
through the Linear MCP server and then records the identity on the work unit.
Concord stores the identity as reported. It makes no Linear call to create,
confirm, or update the issue. One issue identity belongs to one work unit, and
a second claim on the same identity refuses.

### D4. Concord mirrors no planning change

Concord writes no Initiative, Linear Project mapping, membership comment,
revision comment, audit comment, or issue field. The `initiative` work kind
receives no new writes. Initiative grouping is a Linear Project or Linear
Initiative that the operator owns in Linear.

### D5. A pull request names its Linear issue

The body of each pull request for managed work carries one non-closing
`Related to <issue key>` line that names the work unit's recorded issue. A
closing phrase is not used, because issue state changes only through the
Linear MCP server or the operator. GitHub Issues Sync stays off.

### D6. A backlog never resides in a git repository

No route commits a planning backlog entry into a repository. A session that
finds work for another Product records it in that Product's Linear team
through the Linear MCP server. The session creates no Concord work unit for
that work. A Product with no Linear team has no planning route.

### D7. The Concord binary holds no Linear credential

The Concord binary makes no Linear API call. A resume reports the recorded
issue key and URL. The agent reads current issue state through the Linear MCP
server.

### D8. History stays replayable

The event log stays immutable. The event kinds that recorded planning mode and
Initiative entries stay readable for replay, and a new append of those kinds
refuses. A work unit captured with the `initiative` kind stays a readable
historical record with no Initiative behavior. Before the retirement migration
runs, the operator exports Initiative and outbox state for reconciliation in
Linear.

## Alternatives considered

- Repair the mirror: fix outbox reclaim, title limits, and membership comments.
  The operator rejected this route under CON-801, because each repair keeps a
  second planning copy.
- Keep a local-only mode for Products without Linear. This keeps the full
  planning store for one route that no current Product uses.
- Rewrite the event log to drop planning events. This breaks the append-only
  log authority and replay determinism.

## Consequences

- CD-0121, CD-0155, CD-0167, CD-0171, and CD-0188 become superseded. Every
  active workflow contract that pins one of them becomes stale under CD-0036
  D3 and recovers through `supersede_contract` or cancellation.
- `development-authority` is replaced by `managed-development-authority`.
- Deletion removes the planning-mode, Initiative, outbox, Project-link, import,
  backfill, divergence, and drain surfaces end to end.
- An agent that needs planning context must reach the Linear MCP server.
  Concord reads stay correct when Linear is unreachable, but they carry no
  planning facts.

## Verification

- `python3 scripts/check-knowledge-index.py` validates the record graph and the
  supersession relations.
- `python3 scripts/check-pr-linear-link.py` enforces D5 on each pull request.
- The deletion slices carry the runtime evidence for D3, D4, D7, and D8.
  `python3 scripts/check-law-coverage.py` reports the shard outstanding until
  those slices merge.
