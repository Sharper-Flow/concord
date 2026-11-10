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

Follow the shared `records.md` instructions for the Linear writing standard and
placement.

### Example: Linear completion report

Before: a commit SHA and a checklist of commands without a reader-facing result.

After:

> #### Export repair complete
>
> The repair is merged and deployed, so exports now include the selected records;
> the linked PR holds the checks.

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

Follow `records.md` for law changes in the pull request and completion report. For
Concord deletion under CD-0243 D3, name each retired law and its change in meaning, and
cite the retiring clause for each deleted test.

Report what was not done, unfinished work, skipped verification and known
gaps. An accurate partial result is better than a confident complete-sounding
one; the reader must be able to tell what remains unproved.
