---
name: concord-research-writing
description: Use when writing or editing research findings, a research note, or a returned research briefing.
---

# Research writing

Answer the framed question in a cited findings record that a later reader can
trace and check. Separate observed facts from inference and make uncertainty
visible. This method adds no authority, approval or workflow gate under
[skill admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md).
Respect the [workflow contract](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/specs/workflow-engine-contract.md).

## Linear records: writing and placement

Follow the shared `records.md` instructions for the Linear writing standard and
placement.

### Example: research finding

Before: a long issue comment containing every source and diagnostic command.

After:

> #### Investigation complete
>
> The export boundary caused the failure; the linked report holds the evidence. The retry path remains untested.

## Trace the findings

State the framed question and its scope. Rank sources by trust: prefer primary
sources to secondary accounts and source code to prose about its behavior.
Follow each claim to its owning source and cite it at the claim. Verify decisive
claims against those sources rather than relying on their summaries.

Give repository paths and lines, external URLs and versions or dates, and
diagnostic commands and exit codes where used. Label partial inventories. Keep
observations separate from inferences; show conflicting evidence rather than
averaging it away. Name remaining uncertainty, gaps and decisions the caller
must make.
For choosing, writing or reviewing a diagram, load `concord-diagram-writing`.

## Leave a retraceable record

The driving session records the findings and conclusion in the work record. A
returned briefing gives that session the same claim-level evidence. Keep
research with a durable result as one cited research record under
`.concord/docs/` in the Product's designated Git knowledge home, and say where
it is. Where no knowledge home is designated, place it where the requester or
the repository's documentation places such notes.

Put the result and limitations on the ticket and link the full report from the issue
at its accessible owning record. Copy findings into a Linear Document only when
the operator requests it; such a copy is not a second long-term home.

Keep law, architecture reference and product vision in Git, and settled
decisions and consequences on the ticket as `records.md` directs. Keep private
data out of Linear and public repository content.

The reader should be able to follow every claim to its source, understand what
remains unknown and re-run the decisive checks from the recorded details.
