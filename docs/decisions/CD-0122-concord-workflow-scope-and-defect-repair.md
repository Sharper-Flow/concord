# CD-0122: Concord workflow scope and defect repair

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** Concord workflow participation, outside project work, and defect repair
- **Approval:** The operator approved this repository policy.
- **Related:** CD-0089, CD-0102, CD-0104, and CD-0121
- **Amends:** CD-0089 at the scope of Concord development coordination
- **Preserves:** Product law, repository review and merge evidence, worktree
  isolation, and host-owned roles

## Context

CD-0089 authorizes Concord to coordinate its development. Its broad wording can
be read to require every project action to enter Concord workflow. CD-0102 now
scopes native Task authorization to explicitly managed participation.

A defect in Concord workflow can also block normal recording or execution. A
safe repository repair needs a route that does not invent workflow state while
the defect remains.

## Decision

### D1. Workflow participation is explicit

Concord workflow governs project work only after explicit managed participation.
All other project work may proceed under host permissions and repository rules.
Outside work has no Concord workflow authority, evidence, verdict, or completion
claim.

### D2. Defect repair has an outside-work application

When a Concord defect blocks safe normal recording or execution, an authorized
defect repair may proceed outside Concord workflow. The repair uses an isolated
branch and worktree and preserves public pull-request and required-check
evidence. It does not fabricate, rewrite, or infer Concord workflow records.

This application is not a general bypass for managed work. When the managed
workflow route works, managed work follows that route.

### D3. Authority boundaries remain separate

Accepted Product law remains authoritative. Repository pull requests and
required checks remain the review and merge evidence. Git branches and worktrees
remain the implementation boundary.

Host permissions, session identity, session directories, and managed
participation remain host-owned under CD-0102 and CD-0104. A repository policy
change does not change those host roles.

## Consequences

Project work outside Concord workflow remains permitted without receiving
Concord workflow credit. A workflow defect does not force unsafe state writes or
prevent an authorized repository repair.

Outside repair still requires ordinary repository evidence. It does not make a
local assertion proof of merge, and it does not make host behavior repository
authority.

## Rejected alternatives

**Require every project action to use Concord workflow.** Rejected because a
workflow defect can block safe repair and ordinary project work has host-owned
execution rules.

**Let outside work create Concord evidence.** Rejected because workflow evidence
requires managed participation and durable Concord records.

**Change host permissions through repository policy.** Rejected because host
roles require their own owning source and rollout.

## Verification

This is a repository policy amendment. The development-authority contract,
project instructions, knowledge index, document links, public-content checks, and repository
verification checks validate the policy artifacts. No host configuration or
runtime workflow capability changes in this decision.
