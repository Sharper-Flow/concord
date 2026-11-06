---
name: concord-multi-repository
description: Drive a work item that must edit a second Project in another Git repository. Use when a work item needs a second repository: record its Project membership, place a repository-local session in its isolated worktree, and follow placement and handoff refusals' served recovery.
---

# Multi-repository work

This method operates [repository-local sessions](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0178-each-repository-gets-its-own-coordinator-session.md),
[host-owned isolation](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0088-host-owned-work-bootstrap-preserves-pre-readiness-authority.md),
and [Project handoff](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0182-session-opener.md)
under [skill admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md).
It grants no workflow authority.

Keep a work-item session in each repository. First record the second Project
membership with `concord_work_relate.set_memberships`. Host tools create and
place its isolated branch, worktree and session; they grant
no workflow authority. Open that session only when the work must edit the
other repository. The calling directory resolves the Project, and the calling
session stops driving the other repository. Existing Project handoff and
placement checks remain required.

A placement or handoff-bind refusal in the repository you are already in is
not evidence that the work needs a second repository. Follow its served
recovery there and never open a new session to recover from that refusal.
Host tools also own within-repository placement and missing-worktree recovery.
