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

Follow the shared `records.md` instructions for the Linear writing standard and
placement.

### Example: pending timer handoff

Before: a long history of the repair with no timer or resume owner.

After:

> #### Resume handoff
>
> The export repair waits for the verification timer before the next check.
>
> +++ Agent detail
> - Scope: finish the export summary repair under the linked assignment.
> - Timer: pending until 14:00 UTC on 2026-01-02; no check before then.
> - Notification: the verification owner will notify the repair owner at expiry.
> - Stop state: no stop recorded; if a stop arrives, hold until its resume condition is met.
> - After notification, run the assigned check and report to the assigning agent.
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
