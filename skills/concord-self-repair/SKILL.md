---
name: concord-self-repair
description: "Repair a Concord defect that blocks your own managed item. Use when Concord's own defect blocks safe workflow recording or execution of your item: run the CD-0122 D2 authorized repair outside workflow on its own branch and PR, keep the managed item held, and resume through typed operations."
---

# Concord self-repair

Under [CD-0122 D2](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0122-concord-workflow-scope-and-defect-repair.md#d2-defect-repair-has-an-outside-work-application),
carry out the authorized repair outside workflow. This method is shipped under
[skill admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md);
it grants no repair authority.

Use the repair's own isolated branch and worktree. Deliver an ordinary
independently reviewed PR whose body names CD-0122 D2, with required checks.
Make no Concord workflow writes for the repair, fabricate no records, and link
the PR on the Linear issue. Keep the original managed item held until its
supported route works, then resume through typed operations.

The exception permits only the blocking Concord defect repair, not unrelated
work or evasion of a tool refusal, operator hold, denial, or authority boundary.
