---
{
  "id": "compaction-retention-policy",
  "title": "Historical work and explicit research retention",
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
      "exemption": "Cross-tier deduplication and authority composition are query properties. Integrated historical-tier coverage remains unmeasured."
    },
    {
      "criterion": 3,
      "exemption": "Retirement eligibility is an operation property. TestResearchRetirementTerminalOwnersEligible in internal/store/research_retirement_test.go is a candidate anchor; integrated coverage remains unmeasured."
    },
    {
      "criterion": 4,
      "exemption": "Optional-pin protection requires operation witnesses. TestResearchRetirementActivePinsProtectAndTerminalReleaseFences in internal/store/research_retirement_test.go is a candidate anchor; integrated coverage remains unmeasured."
    },
    {
      "criterion": 5,
      "exemption": "Dry-run write absence is an operation property. TestResearchRetirementDryRunMixedBatchHasNoWrites in internal/store/research_retirement_test.go is a candidate anchor; integrated coverage remains unmeasured."
    },
    {
      "criterion": 6,
      "exemption": "Restart and committed replay are operation properties. TestResearchRetireDryRunAndReplay in internal/agent/research_surface_test.go is a candidate replay anchor; restart coverage remains unmeasured."
    }
  ]
}
---
# Concord Historical Work and Explicit Research Retention

## Context

Historical work notes and retained research have readers independent of workflow
execution. Their preservation does not require retaining completed step records.

## Contract

### 1. Current work and historical readers

[PM6 §1](canonical-git-note-placement.md#1-decision) owns reported-link completion,
atomic disposal of completed workflow steps and snapshot-gated upgrade disposal.
There is no publication-gated terminal cleanup, prune command, pressure threshold,
calendar sweep or new archive. Completed work need not acquire a canonical note.
Terminal current-state identity and reported delivery links remain readable.
Operations and effect receipts retain their retry and reconciliation consumers;
finite verified snapshots follow CD-0242 D2 and PM10.

Existing `archived_work` and historical scope edges remain disposable Git-derived
read projections. PM6 preserves their home IDs, note path, commit OID, note
`content_hash`, `lesson_tags`, terminal state/time and frozen scope. A historical
reader does not require a settled publication operation, closure judgement or new
backfill. Removed execution data returns typed absence, not an inspectable step
reference. Historical links never recreate a live execution endpoint.

CD-0142 separately governs per-item nonterminal execution removal after its
planning handoff. No work-removal operation, completion, upgrade or foreign-key
cascade may delete retained research; preserve its owner identity or refuse
before destructive effects.

### 2. Historical population and identity

Historical projections retain these fields from their verified note or stable
canonical-home context:

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

Q2/Q3 union live terminal work and historical work and deduplicate by work ID
before counting, ordering or pagination. Counts use the same population.
Historical filters use frozen Product, Project, Domain and tag associations;
current work retains PM5 membership semantics. Complete authority requires both
the store watermark and applicable historical Git coverage. Incomplete reachable
coverage is degraded with bounded omissions; unreachable Git authority is
unreachable, never authoritative empty.

Q7 reads available current-state records, not an exact event history or process
receipt. Q8 resolves historical supersession from verified recorded metadata.
An already historical identity cannot reopen; renewed need uses a new identity
linked to the old one without changing the historical note.

### 3. Protected research retirement

CD-0215 packs are direct-table context, not historical work or execution steps.
Nonterminal owners may author content; terminal owners retain read-only content
for authorized exact-revision reuse. Scope and freshness govern reliance.
Every required or optional pin belonging to a nonterminal consumer protects the
pack. Consumer terminalization releases its pins transactionally, not the pack.

Completion, durable promotion and a research `no change` outcome preserve packs
and require no consumer release. Selected reasoning may use ordinary accepted
decision, specification or lesson forms; none serializes or indexes the corpus
or promises recovery of a retired pack.

Only explicit `research_retire` deletes a pack aggregate. It is authorized,
bounded to 1–100 distinct candidates, version-fenced, idempotent and transactional.
Dry run reports selected identities and versions without writes. Execution
rechecks terminal ownership and zero active pins. No compaction link,
publication proof, deadline, grace interval, timer, sweep or age establishes
eligibility. Reads, completion, unrelated refusals, upgrade, restore and restart
never retire research.

A destructive batch commits atomically; invalid or unauthorized input leaves
every candidate unchanged. Deletion removes the pack and its dependent revisions,
findings, sources, provenance, scopes and bindings. Crash before commit retains
the aggregate; committed replay returns the original result without repeating
deletion or reconstructing content. Owner reopen before retirement removes
eligibility. Retirement creates no tombstone or deleted-ID registry and is not
secure erasure of backups.

### 4. Retained categories

| Data | Consumer and applicability | Removal condition |
|---|---|---|
| terminal current-state identity and reported delivery link | exact work and delivered-outcome readers | explicit work removal under its owning contract |
| operation results and effect receipts | retries and reconciliation of the original intent | CD-0242 D2's documented end of the retry/reconciliation consumer |
| historical notes and locator projections | historical readers verify recorded Git identity and frozen scope | Git owns notes; projections may be replaced from the same authority |
| research and dependent content | exact-revision readers and active pins under section 3 | explicit eligible research_retire only |
| verified snapshots | PM10 restore and unfinished recovery | documented count/age bound, never the newest verified snapshot or one recovery needs |
| logs, traces, screenshots and raw reports | producer-owned, not Concord authority | the producer's own policy |

No other post-delivery execution category is retained. PM10 owns snapshot aging,
backup topology and reclamation; this contract adds no `VACUUM` or secure-erasure
promise.

## Acceptance criteria

- Given existing historical work
  When upgrade or a historical read runs
  Then its locator, note content hash, lesson tags and frozen scope remain intact.

- Given historical and live work in one population
  When Q2/Q3 counts it
  Then the result deduplicates by work ID and composes authority across both tiers.

- Given a pack with a terminal owner and zero active pins
  When authorized research_retire rechecks its expected version
  Then explicit retirement may delete it without a compaction link or calendar proof.

- Given an active required or optional pin
  When completion, upgrade or research_retire encounters the pack
  Then content remains protected from deletion.

- Given a bounded research retirement batch
  When dry run evaluates it
  Then no content, binding, version or mutation receipt changes.

- Given committed research retirement
  When the authorized idempotency identity replays after restart
  Then it returns the committed result without repeating deletion or reconstructing content.

## Verification

Criterion bindings name the query, upgrade and retirement boundaries that need
evidence. Candidate source anchors do not establish execution or full coverage;
integrated conformance evidence remains outstanding.
