---
name: concord-pr-writing
description: Use when writing or editing a pull request body or completion report for a delivered or partial change.
---

# Pull request writing

Treat the pull request body and completion report as one document family: make
the change clear, tie evidence to its subject, and expose the danger and gaps.
This method adds no authority, approval or workflow gate under
[skill admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md).
Apply [the operator boundary and Concord deletion exemption](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md).

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

### Example: Linear completion report

Before: a commit SHA and a checklist of commands without a reader-facing result.

After:

> #### Export repair complete
>
> Exports now include the selected records. The repair merged and passed its
> checks; the linked PR holds the commit and check details. No operator action
> is needed.

## Show the change

Choose the smallest view that makes the key point clear: a tree, diff sketch,
short call flow or, when smaller, prose. Keep only the boundaries and details
the reader needs. Put each visual beside the explanation it supports.
For choosing, writing or reviewing a diagram, load `concord-diagram-writing`.

Include one non-closing Related to line naming the recorded issue key under
[planning authority](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md).
Link the owning record rather than copying its plan or evidence into a second source
of truth.

## Show evidence and danger

Show before and after evidence appropriate to the change, such as a failing and
passing test or visual evidence. Identify the exact commit and check runs each
result belongs to; never relabel earlier evidence. If a comparison or check is
unavailable, say so instead of implying it ran.

Explain merge danger: whether the change is a one-way door that is hard to
reverse or a two-way door that can be rolled back, and its blast radius. Name
the affected consumers, data or other consequences that matter.

List every law change in both the pull request and completion report with its
record, change in meaning or an explicit statement of none, and reason. For
Concord deletion under CD-0243 D3, name each retired law and its change in meaning, and
cite the retiring clause for each deleted test.

Report what was not done, unfinished work, skipped verification and known
gaps. An accurate partial result is better than a confident complete-sounding
one; the reader must be able to tell what remains unproved.
