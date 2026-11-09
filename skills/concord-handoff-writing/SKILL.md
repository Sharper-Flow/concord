---
name: concord-handoff-writing
description: Use when writing or editing an assignment brief, session handoff, or subagent delegation prompt.
---

# Handoff writing

Give the next agent compact, durable context tailored to its purpose. Point to
the records that own facts rather than copying them into a second account. This
method adds no authority, approval or workflow gate under
[skill admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md).
Respect the [workflow contract](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/specs/workflow-engine-contract.md).

## Linear records: writing and placement

Linear is for people; Concord and git hold agent state. Apply these principles
to new records, not as a template or a reason to rewrite old comments:

1. Lead with two or three plain sentences for someone who has not read the issue:
   what happened, what it means and what is needed, if anything.
2. Use one heading that names the event.
3. Keep agent detail out of the reader's path. Put paths, IDs, commands and counts
   in a collapsed section, opened with +++ Agent detail and closed with +++.
   Prefer a link to a PR, run or Document over pasted evidence.
4. Use bullets for lists, tables for comparisons, and bold only for the one
   thing the reader must not miss.
5. Follow the voice rules: name things, use active voice, avoid a pronoun without
   its noun, restated standing rules and history narration.
6. Use code formatting only for copyable commands, file paths and identifiers,
   not ordinary words.

| Record kind | Home and content |
| -- | -- |
| Problem, scope, end state, decisions | Issue description; current state only. |
| Operator answer | Short Linear comment: question, exact answer and what it decides. |
| Blocker or wait | Linear comment: what is blocked, on whom and what unblocks it. |
| Completion | Linear comment: reader-facing change and links to the PR, release and Document; collapse SHAs and checks or leave them to the PR. |
| Readiness sort or placement | Linear comment: one-sentence verdict and reason. |
| Assignment brief | One Linear comment, human summary first; collapse paths, panes and branch in Agent detail; link standing rules. |
| Rehearsal transcripts, backup IDs, row counts, checksums, test commands | PR body, release or Concord work record, linked; at most a collapsed Evidence section in Linear. |
| Drain acknowledgements, notice receipts and delivery confirmations | Coordination project status update or Concord work record, not per-session issue comments. One RTE status update records each cutover drain. |
| Live progress | Ordinary issue comments, rare: start, blocker and done. A ticket-frontend item instead follows the step-comment rules in `concord-ticket-writing`. |
| Research, audit, inventory | Linear Document linked from the issue. |
| Coordination across streams | Short project status update. |

### Example: assignment

Before: worktree, branch, exclusions and test commands with no explanation.

After:

> #### Assigned
>
> An agent is repairing the export summary. The agent will work only on the
> export files and report back after the checks; no operator action is needed.
>
> +++ Agent detail
> - Worktree: `worktrees/export`; branch: `fix/export`.
> - Write set: `export/summary.go` and `export/summary_test.go`.
> - Check: run the export package tests. Return the result to the assigning agent.
> +++

## Bound the work

State task identity, objective, scope and stop conditions in the assignment.
For a delivery stream, specify its write set, exclusions, checks and return
point: who reports what to whom. Say what the next agent will
be used for and tailor the handoff to that purpose.

An assignment brief attached to a tracked delivery item goes in one assignment
comment on that item, never in the item's description; the description keeps
only the problem, scope, end state and decisions. The brief holds only what is
task-specific: objective, write set, exclusions, checks, stop conditions and
return point, linking the files that own standing rules instead of restating
them. Host locators such as session identifiers and worktree paths belong in
the comment, not the description.

A delegation prompt names the agent's role and points to the assignment rather
than restating it. Name relevant methods when they help the next agent carry out
the assigned work, without granting a new scope or authority.

## Preserve the source of truth

Point to owning files and records by path or URL. Keep standing rules in the
files that own them, and link those files instead of repeating their rules in
the assignment. Move added context to its owning file instead of expanding the
prompt. Preserve unresolved decisions and evidence in their
owning records; link them from the handoff.

Do not duplicate content already captured in specs, plans, issues, commits or
diffs. Redact secrets and private data. A good handoff lets the next agent find
the current assignment, act within its bounds and return the result without
reconstructing a transcript.
