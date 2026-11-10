---
name: concord-ticket-writing
description: Use when writing or editing a Linear issue, a Linear comment or a captured work item, including a defect report, a synthesized finding, or the step and event comments a ticket-frontend item posts on its linked issue.
---

# Ticket writing

Write for a reader who did not witness the work: state the observed problem,
its impact and a checkable outcome, then keep decisions and events in their
owning records. This method adds no authority, approval or workflow gate under
[skill admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md).
Follow [planning authority](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md).

## Linear records: writing and placement

Follow the shared `records.md` instructions for the Linear writing standard and
placement.

### Example: rehearsal result

Before: a store path, a full commit SHA, two backup IDs and six row counts, with
no summary.

After:

> #### Implement complete
>
> The store upgrade is in the linked PR and preserved every row in the rehearsal copy.
>
> +++ Agent detail
> The rehearsal compared the row counts before and after the upgrade; the linked
> work record holds the checks.
> +++

## State the problem and outcome

Before any plan, state the problem with observed evidence, who or what it
affects, and the end state a check or reviewer can decide. Name the error,
path, measurement or operator's words where applicable. Include an exact error or
small reproduction when useful; link full diagnostics. A goal, feature request
or law to implement is not a problem: say what fails, is missing or costs too
much today. Mark unapproved assumptions as unapproved.

For a defect, the title and problem name the failure as the affected party saw
it, not a diagnosis. Put a suspected cause only under a heading marking it
unverified, with evidence for and against it. The end state is the outcome the
affected party needs, not a change to a suspected mechanism. The agent taking
the item reproduces the failure and confirms the cause before designing a fix.

When an issue splits work into several outcomes, plan them with the shipped
`concord-work-shaping` skill and record each outcome's genuine dependencies.

## Keep the record current

The description owns the current problem, impact, scope, success criteria,
consequential decisions and constraints, and links execution detail. When
an agreed decision changes them, patch it in place so concurrent edits survive
and link the approval evidence. A comment holds one event the description
cannot hold: a settled decision and consequence, synthesized finding, blocker,
completion report or, for an item driven through a ticket frontend, a step or
event update.

Read the description and recent comments before commenting. Amend the
description or your own earlier comment instead of restating it. Fold helper
and delegated results into one synthesized finding rather than pasting reports.
The result lets a later reader establish the current issue and its evidence
without reconstructing a conversation. Follow `records.md` for approval evidence,
quotation and owner or assignment comments.

## Post step results on a ticket-frontend item

A reader following an item driven through a ticket frontend sees the linked
issue, not the agent's terminal. Each time a step of the item's plan completes,
post one comment there: identify the step and give one simple visible result line
with links; use headings or bullets as appropriate and collapse why and how.
Follow `records.md` for visible questions, blockers, limitations, delivery states
and ordinary progress comments.
The item's workflow type (change, fix, research or removal) sets the
required steps its plan copies from `contracts/workflow-step-templates.v1.json`:

| Type | Step | The comment states |
| -- | -- | -- |
| all | clarify | decisions recorded, or that no questions remained |
| fix | diagnose | the confirmed cause and its evidence |
| change, fix | plan | outcomes, approach and main risks |
| removal | list | what will be removed, and the retiring clause |
| research | frame | the research question and its bounds |
| research | investigate | the findings |
| change, fix, removal | implement | what changed, with the PR link (the "PR opened" update) |
| change, fix, removal | review | the review outcome (the "review passed" update) |
| research | conclude | the conclusion and any follow-ups |

Questions, blockers and the completion report keep their own rules; post no
other progress comments. Post one comment, outside the step plan, for each of
these events:

- **Stopped without finishing:** why it stopped and what resumes it, as the
  driving role's stop rule already requires.
- **Scope or plan changed after the plan, list or frame step:** what changed,
  why, and the approval link if one was needed.
- **Follow-up filed:** link the new issue.
- **Released or installed separately from merge:** one comment when it actually
  lands.
- **Superseded or cancelled:** what replaces the item, or why it was dropped.

Do not post CI start or watch notices, rebases, or per-review-round comments.
