# Concord Rollout Plan

> **Status:** Public bootstrap and Concord coordination sequence.
> **Authority:** [`goals.md`](./goals.md) records Product direction as guidance;
> [`priorities.md`](./priorities.md) owns the operating envelope, constraints, conflict rules, and standing law.
> This document describes sequencing and entry conditions under those authorities.
> **Origin:** Product direction recorded 2026-07-25; public bootstrap execution
> authorized 2026-08-07 under CD-0007.

## Public boundary

The constitutional snapshot is prepared for
[`Sharper-Flow/concord`](https://github.com/Sharper-Flow/concord), module
[`github.com/sharper-flow/concord`](https://pkg.go.dev/github.com/sharper-flow/concord),
default branch `main`. Public authority begins
at the annotated `constitutional-bootstrap` tag. The candidate does not import
private history, dependency inventories, archive bundles, or synchronization files.

Advance is a public predecessor and lesson source, not a runtime prerequisite or a
second authority. Reachable issue-backed lessons are summarized in
[`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md).

## 1. Work allowed after bootstrap

Development follows [`managed-development-authority.md`](./managed-development-authority.md).
Explicit managed participation grants Concord workflow authority. Outside-workflow
development follows host permissions and repository rules. Planning belongs to
Linear; branches, worktrees, pull requests, and required checks retain their authority.

## 2. Entry conditions

Runtime implementation proceeds only when all of these are true:

1. The public constitutional snapshot is tagged `constitutional-bootstrap`.
2. Concord's development policy is active. Explicit managed participation scopes
   workflow authority, while pull requests, required checks, branches, and
   worktrees retain their authority.
3. CD-0002 fixes SQLite as the sole durable authority; PM2/PM3 fix global scope and
   typed projections.
4. CD-0005, as amended by CD-0042, fixes the bounded generated agent surface and
   deterministic TS1–TS9 evidence contracts before go-live.
5. CD-0006 fixes root Product policy, workflow composition, rigor bands, and
   boundary freshness.
6. CD-0008 fixes mechanism hardening: immutable evidence subjects and typed degradation.
   [CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)
   governs current-state recovery and schema evolution; [D6](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
   governs each attempt's scope, assignment, and subject bindings.
7. CD-0215 fixes research context as bounded working context, not a second
   durable-knowledge authority.
8. The accepted storage-spine conformance slice has a public verification plan.

Missing conditions block runtime implementation but do not block documentation,
research, or clarification work.

## 3. First-usable floor and release evidence

The alpha maturity condition requires the first-usable floor to stay met; this
does not make predecessor replacement current Product direction.

The six **usability-floor** conditions are defined in
[`floor-readiness.md`](./floor-readiness.md) under *First-usable floor*. The
**release-evidence bar** is defined below. Their conditions are linked rather than
restated here.

Two evidence expectations elaborate the usability floor without redefining it.
They serve existing conditions rather than adding conditions of their own:

- TS1–TS9 agent jobs and result envelopes are validated with synthetic scenarios —
  evidence on usability-floor condition 2, agent read/write/execute through tools;
- external systems retain authority for their own execution and enforcement —
  evidence on usability-floor condition 3, workflow types and completion evidence.

### Release-evidence bar

The floor's release-evidence bar requires:

- release, install, privacy, and Linux amd64 evidence meet CD-0007's floor.

## 4. Concord coordination and demand-driven correction

Concord coordinates explicitly managed development.
Outside project work and authorized defect repair remain permitted under host
permissions and repository rules. The first-usable floor is maturity evidence,
not a development gate, migration trigger, or migration plan.

Migration and correction remain demand-driven and ad hoc. Each operation defines
its local scope, authority, input, idempotency, provenance, recovery, and native
execution boundary. The existing predecessor inventory and import commands remain
utilities with their current safeguards.

No global adoption sequence, mandatory pre-adoption exercise, rollback restriction,
retirement sequence, or migration record applies unless a later decision accepts a
bounded need.

[CD-0091](./decisions/CD-0091-maturity-promotion-ladder.md) is one such accepted
bounded need. It owns the evidence bar for maturity and audience-commitment
promotion. It imposes no schedule, so the demand-driven model above still holds.

## 5. Related authority

| Document | Role |
|---|---|
| [`goals.md`](./goals.md) | Product purpose, goals, and measures, as guidance. |
| [`priorities.md`](./priorities.md) | Operating envelope, constraints, conflict rules, and standing law. |
| [`managed-development-authority.md`](./managed-development-authority.md) | Concord development authority, explicit workflow participation, outside-work defect repair, and repository evidence. |
| [`decisions/CD-0007-concord-repository-bootstrap.md`](./decisions/CD-0007-concord-repository-bootstrap.md) | Public repository, bootstrap, governance, release, privacy, and platform boundary. |
| [`decisions/CD-0089-concord-development-coordination.md`](./decisions/CD-0089-concord-development-coordination.md) | Concord development coordination and explicit managed participation. |
| [`decisions/CD-0122-concord-workflow-scope-and-defect-repair.md`](./decisions/CD-0122-concord-workflow-scope-and-defect-repair.md) | Explicit workflow participation, outside project work, authorized defect repair, and host-role preservation. |
| [`decisions/CD-0091-maturity-promotion-ladder.md`](./decisions/CD-0091-maturity-promotion-ladder.md) | Maturity and audience-commitment promotion evidence. |
| [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md) | Public predecessor lessons; reference-only. |
| [`clarifications.md`](./clarifications.md) | Accepted decisions and explicitly deferred questions. |

*Each phase earns the next through public evidence, not calendar pressure.*
