---
name: concord-review
description: Review a change or a completed implementation step against the repository's documented standards and the ticket's end state or plan. Use for intermediate step review, branch or pull-request review, and the final clean review before delivery.
---

# Review

Use this method within the review the caller assigned. It adds no workflow step,
gate, record, or editing authority under
[method admission](https://github.com/Sharper-Flow/concord/tree/cc7b956e6a5b32eaf99ee02ab93122fef71dc70f/.concord/docs/decisions/CD-0246-concord-ships-portable-engineering-methods.md).

## Establish the review

Identify the base, the candidate, the assigned step, and the permitted repair
scope. Include uncommitted changes when they are part of the assigned candidate.
Establish whether the caller requested an intermediate or final review.

Read the ticket's end state or approved plan and the repository's applicable
standards, including contribution guidance and local instructions for affected
files. Follow their references where the change depends on them. Return missing
inputs to the caller rather than inventing requirements from the diff.

## Review two axes

Keep the two assessments distinct; passing one cannot compensate for failing
the other. Neither requires another subagent.

### Repository standards

Compare the change with each applicable documented rule. Read surrounding code
and affected callers where needed to decide whether the rule holds. Cite the
rule for a violation. Keep design concerns separate from documented violations;
explain their concrete cost rather than promoting a preference into a standard.

Use existing automated results for rules they cover. Spend review attention on
what those checks cannot decide, not on duplicating their diagnostics.

### Ticket end state or plan

Account for every outcome the assigned step owes. Identify missing or partial
requirements, implementation that does the wrong thing, and changes beyond the
agreed scope. Trace affected behavior far enough to assess the outcome, including
callers and failure paths outside the diff. For intermediate review, distinguish
work the step owes now from work the plan assigns to a later step.

## Report and repair findings

Report actionable findings in severity order, retaining their axis. Use the
repository's severity scale when it defines one; otherwise use:

- **High:** defeats the change's primary outcome or risks serious incorrect behavior,
  security failure, or data loss.
- **Medium:** a material defect, incomplete requirement, or documented-standard
  violation that needs correction before this step passes.
- **Low:** a limited issue with a concrete consequence, not a stylistic preference.

For each finding, give its location, the rule or outcome it contradicts, the
observed defect and consequence, and the correction. Mark an unproven concern as
an open question, not a confirmed defect.

Repair findings within the caller's authorized scope and verify those repairs.
Return out-of-scope findings and decisions to the caller. If a finding or missing
input prevents a supported pass, return blocked with the exact unresolved need.
Keep low-severity issues visible even when the repository permits them to remain.

## Return the result

Name the reviewed candidate and step, assess both axes, and return the result to
the caller: pass, fixed and pass, or blocked. Include findings by severity, fixes
made, verification of those fixes, and remaining questions or limits. When repairs
change the candidate, identify the resulting subject as well as the reviewed one.

An intermediate review may return fixed and pass after its repairs are verified.
A final review passes only when it needs no fixes. If it fixed anything, return
blocked pending another final review, with the fixes and resulting candidate;
do not report a final pass from the review that made the fixes. The caller owns
that next review and any workflow transition or completion decision.
