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

### Example: whole-project progress

Before: repeated owner completion reports and a table of session telemetry.

After:

> #### Project progress
>
> The project has completed its migration; release remains blocked on the final audit.
>
> - Risk: the audit may delay the project release.
> - Next decision: the release owner decides whether to proceed after the audit.
>
> +++ Agent detail
> - Audit owner and evidence are in the linked delivery record.
> +++

## Put each fact with its owner

The coordination project's description holds the current assigned scope,
cross-stream dependencies, sequencing, directives and drain summary. Project
status updates cover only whole-project progress, major completions,
operator-facing blockers and achievements. Do not use status updates for routine
assignments, acknowledgements, drain records, approval quotes, per-item status
or coordination directives. Link owning delivery records instead of copying
their requirements, evidence or status lists.

The delivery issue owns its acceptance, evidence, blocker,
next action and planning state. Add a coordination fact there only when it
changes that item's execution. Preserve unresolved decisions and evidence in
their existing owning records during a transfer.

Put results and limitations on the ticket and link the full report at its accessible
owning record. Copy findings into a Linear Document only on operator request.
Load `concord-research-writing` for canonical
placement and the writing method.

## Write the update

Read the existing record first. Update it only when an eligible project-level
fact has changed; link or amend the owned fact rather than repeat it. Lead with
the change since the last update, then name any decision needed. Set health and
status from evidence, never invention.

A good update lets each owner see what changed and follow links to the facts
that govern its work without maintaining a second backlog.
