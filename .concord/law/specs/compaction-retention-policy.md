---
{
  "id": "compaction-retention-policy",
  "title": "Historical work retention",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": ["durable-authority"],
  "criterion_bindings": [
    {
      "criterion": 1,
      "exemption": "Historical locator preservation is a read and upgrade property. Integrated evidence must cover archived_work, content_hash, lesson_tags and frozen historical scope."
    },
    {
      "criterion": 2,
      "exemption": "Current-state population composition and unreachable refusal are query properties. Q2/Q3 read retained current-state rows only; no historical-tier union or coverage composition exists to measure."
    }
  ]
}
---
# Concord Historical Work Retention

## Context

Historical work notes have readers independent of workflow execution. Their
preservation does not require retaining completed step records.

## Contract

### 1. Current work and historical readers

[PM6 §1](canonical-git-note-placement.md#1-decision) owns reported-link completion,
atomic disposal of completed workflow steps and snapshot-gated upgrade disposal.
There is no publication-gated terminal cleanup, prune command, pressure threshold,
calendar sweep or new archive. Completed work need not acquire a canonical note.
Terminal current-state identity remains readable. Reported delivery links remain
stored, but have no agent read.
Operations and effect receipts retain their retry and reconciliation consumers;
finite verified snapshots follow CD-0242 D2 and PM10.

Existing `archived_work` and historical scope edges remain retained Git-derived
read rows; Git owns the notes, and no replacement or disposal surface exists
(section 3). PM6 preserves their home IDs, note path, commit OID, note
`content_hash`, `lesson_tags`, terminal state/time and frozen scope. A historical
reader does not require a settled publication operation, closure judgement or new
backfill. Removed execution data returns typed absence, not an inspectable step
reference. Historical links never recreate a live execution endpoint.

CD-0142 separately governs per-item nonterminal execution removal after its
planning handoff.

### 2. Historical population and identity

Historical projections retain these fields from their retained note record or
stable canonical-home context:

```text
id, type, title, completed_at, outcome_tag, lesson_tags,
home_project_id, home_locator_id, note_path, commit_oid, content_hash,
terminal_state, priority, summary,
product_ids[], project_ids[], domain_ids[], tag_ids[], successor_work_id?
```

Scope is frozen at the note's recorded revision. Later memberships do not
rewrite it. `completed_at` is the terminal timestamp for completed, cancelled
and superseded work; PM1's `terminal_at` maps to it. A superseded historical
item's recorded successor remains readable. Historical edges carry no mutable
blocker, readiness, evidence or per-Project status.

Q2/Q3 answer over the retained current-state population with PM5 membership
semantics; neither query unions historical projections, and neither computes a
cross-tier watermark or coverage check. Authority levels on the answer envelope
follow [TS7](agent-result-envelope.md): an OK answer is authoritative over the
store read, and a store that cannot answer refuses as `unreachable` rather than
serving stale or guessed rows.

Q7 reads available current-state records, not an exact event history or process
receipt. Q8 resolves historical supersession from retained recorded metadata.
An already historical identity cannot reopen; renewed need uses a new identity
linked to the old one without changing the historical note.

### 3. Retained categories

| Data | Consumer and applicability | Removal condition |
|---|---|---|
| terminal current-state identity and reported delivery link | exact work readers and retained delivery state | explicit work removal under its owning contract |
| operation results and effect receipts | retries and reconciliation of the original intent | CD-0242 D2's documented end of the retry/reconciliation consumer |
| historical notes and locator projections | historical readers return the recorded locator and authorize it through frozen scope | Git owns notes; retained projections restore as current state ([PM6 §3](canonical-git-note-placement.md#3-historical-locator)), with no replacement surface |
| verified snapshots | PM10 restore and unfinished recovery | documented count/age bound, never the newest verified snapshot or one recovery needs |
| logs, traces, screenshots and raw reports | producer-owned, not Concord authority | the producer's own policy |

No other post-delivery execution category is retained. PM10 owns snapshot aging,
backup topology and reclamation; this contract adds no `VACUUM` or secure-erasure
promise.

## Acceptance criteria

- Given existing historical work
  When upgrade or a historical read runs
  Then its locator, note content hash, lesson tags and frozen scope remain intact.

- Given terminal work retained in the current-state population
  When Q2/Q3 counts or lists it
  Then counts and items come from the retained current-state read alone, with no
  historical union or coverage composition, and an unreachable store refuses as
  `unreachable` rather than returning stale rows.

## Verification

Criterion bindings name the query and upgrade boundaries that need
evidence. Candidate source anchors do not establish execution or full coverage;
integrated conformance evidence remains outstanding.
