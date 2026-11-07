---
name: concord-ticket-writing
description: Use when writing or editing a Linear issue, a Linear comment or a captured work item, including a defect report or a synthesized finding.
---

# Ticket writing

Write for a reader who did not witness the work: state the observed problem,
its impact and a checkable outcome, then keep decisions and events in their
owning records. This method adds no authority, approval or workflow gate under
[skill admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
Follow [planning authority](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md).

## State the problem and outcome

Before any plan, state the problem with observed evidence, who or what it
affects, and the end state a check or reviewer can decide. Name the error,
path, measurement or operator's words where applicable. A goal, feature request
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

The description owns the current problem, scope, end state and decisions. When
an agreed decision changes them, patch it in place so concurrent edits survive
and link the approval evidence. A comment holds one event the description
cannot hold: a quoted operator decision, synthesized finding, handoff, blocker or
completion report.

Read the description and recent comments before commenting. Amend the
description or your own earlier comment instead of restating it. Fold helper
and delegated results into one synthesized finding rather than pasting reports.
The result lets a later reader establish the current issue and its evidence
without reconstructing a conversation.
