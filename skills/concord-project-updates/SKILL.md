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

Follow the shared `records.md` instructions for the Linear writing standard and
placement.

### Example: cross-stream release wait

Before: repeated owner completion reports and a table of session telemetry.

After:

> #### Release wait
>
> The export repair merged, but release waits for the schema check.
>
> - Risk: the schema check may expose an incompatibility.
> - Next decision: the release owner decides whether to proceed after the check.
>
> +++ Agent detail
> - Check owner: schema audit; timing and lifecycle details are in the linked delivery record.
> +++

## Put each fact with its owner

The coordination project's description holds the assigned scope, kept current.
Its status updates carry material cross-stream dependencies, sequencing
decisions and changes to coordination directives. Link owning delivery records
instead of copying their requirements, evidence or status lists.

The delivery issue owns its acceptance, evidence, blocker,
next action and planning state. Add a coordination fact there only when it
changes that item's execution. Preserve unresolved decisions and evidence in
their existing owning records during a transfer.

Put results and limitations on the ticket and link the full report at its accessible
owning record. Copy findings into a Linear Document only on operator request.
Load `concord-research-writing` for canonical
placement and the writing method.

## Write the update

Read the existing record first. Update it only when a planning fact, decision,
risk or evidence has changed; link or amend the owned fact rather than repeat
it. Lead with the change since the last update, then name any decision needed
and the affected streams. Set health and status from evidence, never invention.
Routine progress mirroring and acknowledgement comments add no useful change.

A good update lets each owner see what changed and follow links to the facts
that govern its work without maintaining a second backlog.
