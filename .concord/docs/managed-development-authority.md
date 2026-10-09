# Concord managed development authority

**Status:** Accepted under CD-0213. Supersedes
[`development-authority.md`](development-authority.md).
**Approval date:** 2026-10-09.
**Approval:** Operator approval under
[Concord (CON) issue 801](https://linear.app/sharper-flow/issue/CON-801) for
Linear as the only planning authority.

## Context

Concord owns its workflow for explicitly managed work. Linear owns planning.
[CD-0213](decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md)
makes the Linear MCP server the only route for planning facts and removes the
Concord planning mirror.

[CD-0122](decisions/CD-0122-concord-workflow-scope-and-defect-repair.md)
lets project work proceed outside Concord workflow. It also defines a
defect-repair application when the workflow blocks safe repair.

## Contract

Planning facts live in Linear. Agents read and write them through the Linear
MCP server. Concord keeps no planning copy, no planning mode, and no outbox.

Concord workflow governs only explicitly managed participation. A managed work
unit carries at most one Linear issue identity that the agent records after it
creates or finds the issue in Linear. Concord makes no Linear call.

Project work is permitted outside the workflow under host permissions and
repository rules. Work outside the workflow creates no Concord workflow
authority, evidence, verdict, or completion claim.

An authorized defect repair may proceed outside Concord workflow when a Concord
defect blocks safe normal recording or execution. The repair uses an isolated
branch and worktree and preserves public pull-request and required-check
evidence. It does not fabricate, rewrite, or infer Concord workflow records.

Host permissions, session identity, session directories, and managed
participation remain host-owned. This contract does not change host roles.

### Authority by fact type

| Fact or action | Authority | Evidence |
|---|---|---|
| Planned work, defects, backlog, and grouping | Linear, through the Linear MCP server | The Linear issue or Project identity |
| Work found for another Product | That Product's Linear team, through the Linear MCP server | The Linear issue identity; no serving-Product Concord work unit |
| Managed work unit and its Linear issue link | Concord | Durable work identity and the recorded issue key, UUID, and URL |
| Workflow transitions, verdicts, and completion | Concord | Session identity and typed operation records |
| Review and merge | Repository pull requests and required checks | Review decisions, check results, merge record, and changed files |
| Implementation isolation | Git branches and worktrees | Branch ancestry, worktree boundaries, and commits |
| Product law | Accepted decisions, specifications, and constitutional documents | Canonical knowledge records, approved versions, and law relations |
| Predecessor lessons | Public predecessor records | Reachable citations in [advance-predecessor-lessons.md](advance-predecessor-lessons.md) |

### Preserved boundaries

1. Planning records do not silently amend accepted Product law.
2. Concord's public repository retains public pull-request and required-check
   evidence. A local assertion is not proof of merge.
3. Implementation uses an isolated branch and worktree, not the default checkout.
4. Each pull request for managed work carries one non-closing
   `Related to <issue key>` line that names the recorded issue.
5. Advance remains reference-only. Concord writes no predecessor state.
6. Public repository content excludes private Product data, credentials, and
   inaccessible private-source citations.
7. Concord workflow authority applies only to explicitly managed participation.
8. A defect repair outside workflow retains repository isolation and public
   review evidence without changing host roles or Product law.
9. No route commits a planning backlog entry into a repository.

## Acceptance criteria

- Given an agent records planned work or a defect
  When the agent writes the record
  Then the record goes to Linear through the Linear MCP server, and Concord stores no planning copy.

- Given a claim that a Concord change merged
  When the claim is checked
  Then the public pull request and required checks supply the merge evidence.

- Given implementation work
  When an agent writes it
  Then it resides in an isolated branch and worktree, not the default checkout.

- Given a proposed change that conflicts with accepted law
  When review examines it
  Then the conflict requires an explicit law amendment rather than silent narrowing.

- Given a managed work unit with a recorded Linear issue
  When its pull request opens
  Then the body names the issue with one non-closing reference.

- Given a workflow transition, verdict, or completion claim
  When the operation succeeds
  Then Concord records the operation in its durable authority.

- Given Concord development work
  When the workflow records its state
  Then Concord writes no Advance state.

- Given a session that serves one Product and finds work owned by another Product
  When the session records the work
  Then it records the work in the other Product's Linear team and creates no Concord work unit.

- Given project work without explicit managed participation
  When the work is performed
  Then host permissions and repository rules govern it without Concord workflow evidence.

- Given a Concord defect blocks safe normal recording or execution
  When an authorized repair occurs outside workflow
  Then it uses an isolated branch and worktree and preserves public review evidence.

- Given any route that records planning-backlog state
  When the route is examined
  Then it writes no backlog entry into a git repository.

## Verification

- Criterion 1: CD-0213 D1 owns the rule, and
  `python3 scripts/check-knowledge-index.py` validates its supersession graph.
  The deletion slices remove every Concord planning write route.
- Criterion 2: a merge claim requires the public pull request and required
  checks. `python3 scripts/check-doc-links.py` validates the cited records only.
- Criterion 3: worktree occupancy isolation is exercised by
  `internal/store.TestClaimWorktreeStillRefusesForeignWorkOccupancy`.
- Criterion 4: `python3 scripts/check-knowledge-index.py` validates the
  declared law graph.
- Criterion 5: `python3 scripts/check-pr-linear-link.py` enforces the
  non-closing reference on each pull request.
- Criterion 6: `python3 scripts/check-agent-contracts.py` validates the
  generated contracts, and `internal/store.TestAppendEventRoundTripsEveryField`
  covers the event records.
- Criterion 7: `python3 scripts/check-predecessor-independence.py` checks
  repository-owned agent surfaces.
- Criterion 8: CD-0213 D6 owns the foreign-Product route, and Concord has no
  write path for it. `python3 scripts/check-knowledge-index.py` validates the
  record that owns the route.
- Criterion 9: review against CD-0122 D1.
  `python3 scripts/check-knowledge-index.py` validates the cited record.
- Criterion 10: review against CD-0122 D2. Isolation mechanics are exercised by
  `internal/store.TestClaimWorktreeStillRefusesForeignWorkOccupancy`.
- Criterion 11: the store refuses an in-repository database path in
  `cmd/concord.TestDatabaseOverrideRefusesRepositoryLocalPath`.
