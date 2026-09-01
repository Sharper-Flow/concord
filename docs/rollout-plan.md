# Concord Rollout Plan

> **Status:** Public bootstrap and Concord coordination sequence.
> **Authority:** [`priorities.md`](./priorities.md) owns the ranked priorities and
> operating envelope; this document owns sequencing and entry conditions.
> **Origin:** Product direction recorded 2026-07-25; public bootstrap execution
> authorized 2026-08-07 under CD-0007.

## Public boundary

The constitutional snapshot is prepared for `Sharper-Flow/concord`, module
`github.com/sharper-flow/concord`, default branch `main`. Public authority begins
at the annotated `constitutional-bootstrap` tag. The candidate does not import
private history, dependency inventories, archive bundles, or synchronization files.

Advance is a public predecessor and lesson source, not a runtime prerequisite or a
second authority. Reachable issue-backed lessons are summarized in
[`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md).

## 1. Work allowed after bootstrap

After issue #600 ships, the following may proceed through Concord's workflow with
GitHub Issues, branches, worktrees, pull requests, and required checks:

- refine constitutional docs, specifications, and decision records;
- implement the accepted storage/core slice after bootstrap;
- build contract validators and synthetic conformance scenarios;
- develop the agent adapter and workflow types within accepted decisions;
- run public-source research and record bounded, dated findings;
- improve documentation links, examples, and release evidence.

The authority model for this work is accepted in
[`development-authority.md`](./development-authority.md) and CD-0089.

## 2. Entry conditions

Runtime implementation proceeds only when all of these are true:

1. The public constitutional snapshot is tagged `constitutional-bootstrap`.
2. Concord's development workflow is active, with GitHub Issues, pull requests,
   required checks, branches, and worktrees retaining their authority.
3. CD-0002 fixes SQLite as the sole durable authority; PM2/PM3 fix global scope and
   typed projections.
4. CD-0005, as amended by CD-0042, fixes the bounded generated agent surface and
   deterministic TS1–TS9 evidence contracts before go-live.
5. CD-0006 fixes root Product policy, workflow composition, rigor bands, and
   cross-workflow impact propagation.
6. CD-0008 fixes mechanism hardening: immutable evidence subjects, typed degradation,
   attempt fencing, external conditions, and schema/history evolution.
7. CD-0009 fixes active research context as bounded working context, not a second
   durable-knowledge authority.
8. The accepted storage-spine conformance slice has a public verification plan.

Missing conditions block runtime implementation but do not block documentation,
research, or clarification work.

## 3. Replacement-ready floor

Concord is not replacement-ready after a partial dashboard, isolated tool, or
single workflow. The full floor must be proven for one operator and many concurrent
agents on one machine.

Replacement readiness is two bars: the **usability floor**, defined by the numbered
conditions in [`priorities.md`](./priorities.md) *First-usable floor*, plus the
**release-evidence bar** this section owns. `priorities.md` remains authorizing for
the usability floor. Its conditions are linked rather than restated here, because
restating them is how the two documents drifted.

Two evidence expectations elaborate the usability floor without redefining it. They
are decomposed as manifest items under the conditions they serve rather than as
conditions of their own:

- TS1–TS9 agent jobs and result envelopes are validated with synthetic scenarios —
  evidence on usability-floor condition 2, agent read/write/execute through tools;
- external systems retain authority for their own execution and enforcement —
  evidence on usability-floor condition 3, workflow types and completion evidence.

### Release-evidence bar

Beyond the usability floor, replacement readiness additionally requires:

- release, install, privacy, and Linux amd64 evidence meet CD-0007's floor.

Both bars are decomposed in the floor manifest, and each condition's `source` names
the document and section that bears it.

## 4. Concord coordination and demand-driven correction

Concord coordinates its own development after issue #600 ships. The replacement
readiness floor is an evidence claim, not a development gate, migration trigger,
or migration plan.

Migration and correction remain demand-driven and ad hoc. Each operation defines
its local scope, authority, input, idempotency, provenance, recovery, and native
execution boundary. The existing predecessor inventory and import commands remain
utilities with their current safeguards.

No global adoption sequence, mandatory pre-adoption exercise, rollback restriction,
retirement sequence, or migration record applies unless a later decision accepts a
bounded need.

[CD-0091](./decisions/CD-0091-maturity-promotion-ladder.md) is one such accepted
bounded need. It defines the evidence bar that promotes a Product's maturity and
audience commitment above the replacement-ready floor. It imposes no schedule, so
the demand-driven model above still holds.

## 5. Related authority

| Document | Role |
|---|---|
| [`priorities.md`](./priorities.md) | Canonical priorities, operating envelope, and replacement floor. |
| [`development-authority.md`](./development-authority.md) | Concord development workflow with GitHub planning and merge authority. |
| [`decisions/CD-0007-concord-repository-bootstrap.md`](./decisions/CD-0007-concord-repository-bootstrap.md) | Public repository, bootstrap, governance, release, privacy, and platform boundary. |
| [`decisions/CD-0010-pre-readiness-development-authority.md`](./decisions/CD-0010-pre-readiness-development-authority.md) | Historical pre-readiness authority, superseded only by CD-0089 at its self-hosting boundary. |
| [`decisions/CD-0089-concord-development-coordination.md`](./decisions/CD-0089-concord-development-coordination.md) | Concord development coordination after the issue #600 bootstrap exception. |
| [`decisions/CD-0091-maturity-promotion-ladder.md`](./decisions/CD-0091-maturity-promotion-ladder.md) | Maturity and audience-commitment promotion ladder above the replacement-ready floor. |
| [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md) | Public predecessor lessons; reference-only. |
| [`clarifications.md`](./clarifications.md) | Accepted decisions and explicitly deferred questions. |
| [`storage-spine-slice.md`](./storage-spine-slice.md) | First implementation acceptance slice. |

*Each phase earns the next through public evidence, not calendar pressure.*
