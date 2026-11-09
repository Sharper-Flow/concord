---
name: concord-project-updates
description: Use when writing or editing a Linear project description or status update for coordination across delivery streams.
---

# Project updates

Keep coordination records current without turning them into copies of delivery
records. Lead an update with what changed, then any decision needed. This method
adds no authority, approval or workflow gate under
[skill admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md).
Follow [planning authority](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md).

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

### Example: cutover drain

Before: one issue comment per session with a pane ID and acknowledgement.

After:

> #### Cutover drain complete
>
> All three sessions drained by 07:55 UTC, so the cutover owner can proceed. No
> operator action is needed.
>
> - Export repair session: drained at 07:41 UTC.
> - Schema audit session: drained at 07:48 UTC.
> - Release notes session: drained at 07:55 UTC.

## Put each fact with its owner

The coordination project's description holds the assigned scope, kept current.
Its status updates carry material cross-stream dependencies, sequencing
decisions and changes to coordination directives. Link owning delivery records
instead of copying their requirements, evidence or status lists.

The delivery issue owns its acceptance, evidence, execution owner, blocker,
next action and planning state. Add a coordination fact there only when it
changes that item's execution. Preserve unresolved decisions and evidence in
their existing owning records during a transfer.

During development, copy coordination research, audit and inventory findings
into a Linear Document attached to the owning issue or project. Link the Document
from the issue and the issue from the Document. This is a development-time copy,
not a second long-term home. Load `concord-research-writing` for canonical
placement and the writing method.

## Write the update

Read the existing record first. Update it only when a planning fact, decision,
risk or evidence has changed; link or amend the owned fact rather than repeat
it. Lead with the change since the last update, then name any decision needed
and the affected streams. Set health and status from evidence, never invention.
Routine progress mirroring and acknowledgement comments add no useful change.

A good update lets each owner see what changed and follow links to the facts
that govern its work without maintaining a second backlog.
