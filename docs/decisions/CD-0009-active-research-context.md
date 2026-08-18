# CD-0009: Active Research Context and Epic Shape

**Status:** Accepted
**Date:** 2026-08-07
**Decision owner:** Operator
**Accepted by operator:** 2026-08-07
**Scope:** Epic identity, C7 research tracking, active research-pack data,
staleness/revision behavior, archive promotion, and deletion.
**Amended by:** CD-0041 replaces the Product-facing Epic term and D1/D1a shape
with secondary Initiative context; D2–D8 remain binding.
**Amends:** CD-0002/PM3's event-only live-state boundary for one explicitly
retention-bounded WIP fact type; PM6/PM7 archive compaction; C7; the Phase 2 Epic
shape question.

## Context

Research is useful while an Epic/change is active, but most research expires. Keeping
complete packs in Git would turn stale working context into permanent Product
knowledge, while retaining their full bodies in `domain_events` would preserve them
indefinitely under a different name. Neither matches the operator's desired lifecycle.

Concord therefore distinguishes operational Product/work facts from active research
context. Product/work facts remain event-sourced. Research packs are durable across
active sessions and crashes, but are deliberately deleted after archive compaction.

## D1. Epic is a canonical work-item kind

An Epic is `work_items.kind = epic`: a finite Product-scoped initiative using the
accepted PM4 lifecycle and PM5 Product/Project scope. An Epic must derive exactly one
Product; its entries derive that same Product while remaining free to span its member
Projects. Epic entries are non-Epic canonical work items connected with the existing
acyclic `parent` relation and projected through bounded
`epic_entries(epic_work_id, child_work_id, position, required)`. New entries default
to `required=true`; optional entries are explicit. The append/fold operation owns
parent/order/required changes atomically; these fields are not copied into child work.

Create/add-entry operations reject an Epic whose PM5 scope does not derive exactly one
Product or a child outside that Product. An Epic is not another Product, table-level
authority, nested workflow runtime, nested Epic, or
container that executes children. Every child retains independent workflow,
authorization, recovery, and terminal state. Epic completion requires every
`required=true` child/condition to be terminal or explicitly removed. Removing an
entry removes its parent/entry projection atomically and never cancels the child.

### D1a. Epic narrative is a living artifact (amended 2026-08-11, issue #47)

An Epic carries a bounded coordination narrative (`work_items.narrative`, at most
16384 characters, empty until first revised). The narrative is the operator's entry
point for understanding the initiative and goes stale precisely while the initiative
is active; a create-only narrative inverts its value. The narrative therefore mutates
through one first-class operation, parallel to entry membership:

- `epic.narrative_revised` is the only narrative write path. It requires non-empty
  bounded text, a non-empty revision reason, and the expected-version fence, and it
  rejects any non-Epic subject before state changes.
- Actor, reason, and text land in `domain_events`; the projection is rebuild-
  deterministic from the log. A terminal Epic may still receive a narrative revision
  so the historical record can be corrected.
- The narrative rides the single-work read path only; bounded list reads keep their
  fixed shape.
- The narrative is a manual summary. No automatic narrative synthesis from entries,
  and no copying of narrative into child work.

## D2. C7 resolves to ordinary work items

C7 selects Option A:

- independent research is `work_items.kind = research` using the registered
  research/investigation workflow;
- embedded discovery/research remains inside its implementation/break-fix/spike work
  item and does not create another trackable;
- architecture spikes remain distinct because they must publish an accepted binding
  decision, while research may conclude `no change`;
- a research pack is active context/output owned by work, not another work item or
  lifecycle.

## D3. Active research-pack aggregate

Research packs live only in the global local Concord SQLite database while their
owner is nonterminal.

```text
active_research_packs
- pack_id
- owner_work_id                 # FK to nonterminal work item
- current_revision
- freshness                    # current | stale | unknown
- expected_version
- created_at
- updated_at

active_research_revisions
- pack_id + revision            # composite PK
- question
- scope_in_json
- scope_out_json
- done_when_json
- method
- created_at

active_research_findings
- pack_id + revision + finding_id
- kind                         # observation | inference | hypothesis |
                               # conclusion | recommendation
- statement
- confidence                   # low | medium | high
- freshness                    # current | stale | unknown
- status                       # active | contradicted | superseded
- scope_mode                   # home | explicit; see CD-0022

active_research_finding_scopes
- pack_id + revision + finding_id + scope_kind + scope_id
- scope_kind                   # product | project | domain | tag

active_research_sources
- pack_id + revision + source_id
- kind                         # official_doc | source_code | issue | paper |
                               # web | local_evidence
- locator
- title
- publisher_or_author
- published_at?
- accessed_at

active_research_finding_sources
- pack_id + revision + finding_id + source_id

active_research_consumers
- pack_id + revision + consumer_work_id
- use_role                     # context | design_input | verification_basis |
                               # decision_basis
- required
- accepted_at
```

All enum values are closed and schema-validated. Finding/source links use composite
foreign keys. CD-0022 as amended by CD-0041 adds the durable-knowledge applies-to
vocabulary at finding level: `home` carries no explicit scopes and `explicit`
carries declared Product, Project, Domain, and tag scopes. Pack/revision IDs are generated and never intentionally reused, but no
permanent tombstone or deleted-ID registry remains after deletion.

## D4. Authority and mutation boundary

Active research is an explicit exception to PM3's retained event-fold boundary:

- the active tables are the sole authority for research-pack content;
- pack bodies, findings, sources, and consumer links never enter retained
  `domain_events`;
- pack tables are not Product-memory projections and are not rebuilt from the domain
  log;
- every write goes through one pack-operation boundary with SQLite transaction,
  expected version, idempotency identity, FK/enum validation, and postcondition
  readback;
- PM10 backup/restore includes active tables while they exist, but no special research
  retention or secure-erasure promise is introduced. Older backups age out under the
  ordinary backup policy.

This is not a second store or authority for the same fact. `domain_events` continues
to own Product/work identity, lifecycle, membership, relations, gates, and archive
linkage. Active pack tables own only disposable working research context.

## D5. Three ownership contexts

### Embedded in one change

The change owns the pack. It has no independent lifecycle. It is deleted when that
change archives after selected durable reasoning is published in the ordinary work
note.

### Independent research inside an Epic

The research work item is a child of the Epic and owns its pack. Consumers link to an
exact active revision. A `blocks` edge is added only when research completion is
actually required; nonblocking reuse is represented only by a consumer binding.

### Epic-level shared research

The Epic owns one or more packs. Child changes bind to exact revisions, so later
updates never silently rewrite their context. The pack survives child completion while
the Epic remains active and is deleted when the Epic archives.

## D6. Revision, staleness, and deletion

- Revisions are monotonically increasing integers, not semantic versions.
- Active consumers pin exact revisions.
- Updating a pack creates a revision; it does not rewrite a revision already consumed.
- Old revisions may be deleted when they are neither current nor referenced by any
  active consumer.
- Staleness is explicit (`current | stale | unknown`), never inferred from a timer.
- A required consumer cannot proceed on stale/unknown research unless its workflow
  explicitly accepts/rebinds that context.
- The check is a deterministic fail-closed consequential-boundary query joining the
  pinned consumer binding to its revision/current freshness. It is not a PM4 blocker
  and no heuristic can satisfy it.
- An active required consumer means `required=true` and nonterminal. Pack deletion
  refuses while one remains bound. The caller
  must rebind, remove the requirement, or terminalize the consumer first.
- Consumer terminal/archive removes its active pack bindings before PM7 projection
  pruning. A research item that concludes `no change` cannot archive while required
  consumers remain; they must explicitly remove/rebind first.
- Deletion removes the pack and all revisions/findings/sources/bindings. No tombstone,
  hidden history, Git research file, archived pack row, or research search result
  remains.

## D7. Archive compaction and durable promotion

Research itself is never promoted. Archive/release selects only facts that have an
accepted durable reader:

| Active research output | Durable destination |
|---|---|
| binding choice | separate decision record |
| requirement/invariant | spec |
| reusable operational lesson | lesson |
| change/Epic reasoning and outcome | ordinary PM6 work note |
| temporary evidence, source list, abandoned hypothesis | delete |

Archive order is monotonic:

1. select and review the bounded reasoning/outcomes worth retaining;
2. write the normal PM6 note and any separately accepted decision/spec/lesson;
3. commit and verify Git authority;
4. record normal compaction/archive linkage in SQLite;
5. only then delete every active research pack owned by that work item and remove its
   consumer bindings in one local transaction;
6. if any earlier step fails, retain the packs and resume the same durable operation.

If compaction linkage commits but deletion is interrupted, the same operation—and any
later archive or pack operation—detects the terminal linked owner and idempotently
finishes deletion. This is reconciliation of unfinished cleanup, not a retained
tombstone or polling worker.

The PM6 note may cite selected public sources or summarize important reasoning, but it
must not serialize the pack, enumerate every source/finding, or claim the deleted pack
is recoverable.

## D8. Explicit non-goals

- No active research pack content, findings, sources, or runtime research
  output in Git, under any path — and no `research` durable-knowledge kind.
  *(Amended 2026-08-15, issue #119: the original wording — "no `docs/research/`
  path" — forbade a directory that already held R1–R7, durable design evidence
  feeding accepted CD decisions, including R2 accepted by CD-0006 R2 one day
  before this decision. The prohibition was always meant for pack content and
  runtime output, not for the word "research" in a path. R1–R7 remain durable
  design evidence; they are not pack content and are not indexed as a
  `research` knowledge kind.)*
- No research-pack tombstones, archived pack index, hidden history, or Git copies.
- No raw web pages, screenshots, logs, traces, binaries, or content-addressed store.
- No RDF/PROV-O, RO-Crate, CRDT, semantic versioning, or research mailbox.
- No automatic conversion of a research conclusion into a binding decision.
- No nested Epic/change workflow execution.

## Required conformance scenarios

1. Embedded research survives process/session restart while its change is active.
2. Two agents editing one pack use expected-version conflict/retry; no lost update.
3. Child changes pin different Epic-pack revisions without silent rebinding.
4. A stale required revision prevents the consuming workflow from claiming its
   research prerequisite is satisfied.
5. A nonblocking consumer does not create a PM4 blocker.
6. Archive failure before verified Git publication leaves all active research intact.
7. Successful archive retains selected reasoning in the normal note, then removes all
   owned pack/revision/source/finding/binding rows.
8. Deleted pack text is absent from Git, retained `domain_events`, historical indexes,
   and knowledge search.
9. An architecture spike cannot complete from a research conclusion alone; it still
   requires its separate reviewed/accepted decision record.
10. An Epic cannot complete while a required active child/condition remains.
11. Epic entry reorder/requiredness change preserves one parent edge, one unique
    position per child, and one shared Product scope without rewriting child work.
12. Epic creation rejects ambiguous/multi-Product scope, cross-Product children, and
    nested Epic entries.
13. A research item concluding `no change` cannot archive until required consumers
    explicitly remove or replace their bindings.
14. A crash after compaction linkage but before pack deletion is completed
    idempotently on resume/next boundary, leaving no orphan active context.
15. An Epic narrative revision applies through `epic.narrative_revised` with audit
    actor/reason, survives event-log rebuild, rejects non-Epic subjects and stale
    expected versions, and leaves prior narrative history in `domain_events`.
