# CD-0214: Retained research context with explicit retirement

- **Status:** Accepted
- **Date:** 2026-10-09
- **Approval:** `CON-816` A1, accepted under operator delegation on 2026-10-09;
  calendar expiration was rejected.
- **Supersedes:** [CD-0009](CD-0009-active-research-context.md)
- **Preserves:** [CD-0041](CD-0041-architecture-bound-product-law.md) Initiative
  context, [CD-0022](CD-0022-active-research-finding-scope.md) applicability,
  PM3 event authority, PM10 recovery, and separate durable promotion.
- **Related:** [CD-0215](CD-0215-research-surface-and-explicit-retirement.md),
  [CD-0216](CD-0216-publication-and-retention-boundaries.md),
  [compaction-retention-policy](../compaction-retention-policy-successor.md)

## Context

Research belongs to work but does not acquire a work lifecycle of its own.
Owner completion ends content authoring, not the usefulness of an exact research revision.
Publication must not destroy content that another work item still uses.
An optional pin protects stored content without creating a workflow blocker.

This record replaces the complete research obligations of CD-0009.
CD-0041 remains the authority for Initiative identity, narrative, entries, architecture, and migration.
This replacement does not restore Epic identity or alter unrelated Initiative obligations.

## Decision

### D1. Initiative remains secondary business context

An Initiative is finite and Product-scoped, with a living narrative and ordered entries with explicit requiredness.
It may span Projects and Domains in its Product.
Entries retain independent workflow, authorization, recovery, architecture binding, and terminal state.
Initiative membership does not own architectural compatibility, dependency truth, law placement, or the primary browse path.
CD-0041 D8 and D9 govern its shape and bounded migration.

### D2. Research resolves to ordinary work

- Independent research uses `work_items.kind=research` and the registered investigation workflow.
- Embedded research stays inside its implementation, break-fix, or spike owner without another trackable.
- An architecture spike still requires a separate reviewed and accepted decision; research may conclude `no change`.
- A research pack is context owned by work, not another work item or workflow runtime.

### D3. One direct-authority aggregate

Packs live only in the local Concord SQLite database until explicit retirement.
A nonterminal owner may author content.
A terminal owner retains read-only content that remains available for authorized revision-specific reuse.

The aggregate retains seven authoritative tables:

```text
active_research_packs
- pack_id
- owner_work_id                 # foreign key to owning work, including terminal work
- current_revision
- freshness                    # current | stale | unknown
- expected_version
- created_at
- updated_at

active_research_revisions
- pack_id + revision            # composite primary key
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
- scope_mode                   # home | explicit

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

Enums are closed and schema-validated.
Finding/source links use composite foreign keys.
CD-0022 and CD-0041 govern finding applicability: `home` has no explicit scopes; `explicit` declares Product, Project, Domain, or tag scopes.
Pack and revision identities are never intentionally reused.
Deletion creates no permanent tombstone or deleted-ID registry.

### D4. Research and work have different authority

- These tables are the sole authority for pack content and consumer links.
- Pack bodies, findings, sources, and consumer links never enter retained `domain_events`.
- Pack tables are not work projections and cannot rebuild from the domain log.
- Writes use one transactional pack boundary with expected versions, idempotency, foreign-key validation, enum validation, and postcondition readback.
- Agent envelopes compose the same transaction-scoped cores without a second idempotency owner.
- PM10 backup and restore preserve these tables while they exist.
- Ordinary backup aging remains separate; retirement is not secure erasure of backup copies.

`domain_events` still owns work identity, lifecycle, membership, relations, gates, and archive linkage.
No second database, corpus event log, or work-context projection replaces direct research authority.

### D5. Three ownership contexts

An individual change owns its embedded pack.
Independent research within an Initiative owns its own pack; consumers bind exact revisions.
A `blocks` edge represents required research completion, not nonblocking reuse.
An Initiative may own shared packs whose child consumers bind different revisions.
Child completion does not complete the owner or silently rebind another child.
All three contexts use the same terminal-owner retention rule.

### D6. Revisions, freshness, and protection

- Revisions increase monotonically; they are not semantic versions.
- A consumer pins an exact revision; later revisions never silently replace its content.
- A revision already consumed cannot be rewritten.
- Nonterminal authoring may prune an old revision only when it is neither current nor pinned by any active consumer.
- Terminal-owner content remains read-only; authoring and revision pruning refuse.
- Freshness is explicit (`current`, `stale`, or `unknown`), not inferred from owner completion or time.
- An authorized freshness update may mark retained content stale without rewriting its content.
- Required stale or unknown research refuses consequential progress unless the workflow explicitly accepts or rebinds it.
- The engine checks stored required pins at the consuming boundary, even when the caller omits new declarations.
- This deterministic fail-closed query is not a PM4 blocker and admits no heuristic substitute.
- An active pin belongs to a nonterminal consuming work item, whether `required=true` or `required=false`.
- Every active pin protects its pack from retirement.
- Consumer terminalization releases its active bindings transactionally before any work-projection removal.
- Publication and a `no change` conclusion do not require consumers to release their pins.
- New authorized reliance on a retained terminal-owner revision remains valid while the pack exists.
- Scope, exact revision identity, and freshness still govern that reliance.

### D7. Publication and retirement are separate

Publication selects bounded durable reasoning, writes the ordinary PM6 note or separately accepted decision, specification, or lesson, verifies Git, and records linkage.
Publication failure retains the pack; successful publication also retains it.
No research conclusion automatically becomes law.

| Selected research output | Durable destination |
|---|---|
| binding choice | separate accepted decision |
| requirement or invariant | accepted specification |
| reusable operational lesson | lesson |
| change or Initiative reasoning and outcome | ordinary PM6 work note |
| temporary evidence, source list, abandoned hypothesis | no automatic durable promotion |

Only an explicit `research_retire` call deletes a pack aggregate.
Eligibility requires a terminal owner and zero active pins, including optional pins.
Owner archive or verified compaction linkage is not a retirement prerequisite.
There is no calendar deadline, grace interval, timer, sweep, or age-derived eligibility.
Eligibility alone never performs deletion.

One authorized batch contains 1–100 distinct candidate identities with expected versions.
The caller may obtain a bounded candidate list through a dry run that writes nothing.
The destructive transaction rechecks authorization, owner lifecycle, expected versions, and all active pins.
Its result identifies retired and protected candidates and version conflicts without an unbounded scan.
All effects of one batch commit atomically; an invalid or unauthorized batch leaves every candidate unchanged.
Retirement deletes the pack and all dependent revisions, findings, sources, provenance, scopes, and bindings.
Crash before commit retains the aggregate; crash after commit retains the committed result.
Replay returns that result without reconstructing content or repeating deletion.

Reads, failed unrelated mutations, terminal hooks, publication, rebuild, and restart do not perform retirement.
Any authorized owner reopen before retirement removes terminal eligibility in the same transaction.
Owner removal and foreign-key cascades must preserve retained research or refuse removal, never bypass explicit retirement.
No deleted pack is reconstructed from a work note or event history.

### D8. Non-goals and content boundaries

- No runtime pack corpus, findings, sources, or output in Git under any path.
- Durable design evidence remains distinct from runtime pack content.
- No research durable-knowledge kind, archived pack index, tombstone, hidden history, or recovery promise from work history.
- No raw webpage, screenshot, log, trace, binary, or content-addressed research store.
- No RDF/PROV-O, RO-Crate, CRDT, research semantic version, mailbox, or nested Initiative workflow.
- Shared context may carry references and bounded selected source-qualified reasoning, not a serialized corpus or alternate storage authority.

## Alternatives considered

- Archive-driven deletion couples publication to destruction and prevents reuse of a terminal owner's content.
- Required-pin-only protection discards optional content that an active consumer still uses.
- Calendar expiration adds an unmeasured time policy and conflicts with the accepted no-calendar authority.
- Read-side cleanup hides destructive effects inside inspection and unrelated refusals.
- A second research event log preserves disposable bodies indefinitely and duplicates direct authority.

## Consequences

Terminal packs can remain indefinitely until an authorized explicit call retires them.
This policy gives bounded per-call work, not a physical-deletion deadline.
The parent implementation must remove archive-driven and read-side deletion in the same cutover.
Migration must preserve bodies, provenance, revisions, pins, freshness, versions, and retained-owner identity.
Mixed binaries must refuse incompatible storage or operation semantics before effects.

CD-0036 governs the breaking replacement and its consumer recovery.
Deployment must enumerate active consumers of CD-0009 and recover each contract to CD-0214, or cancel or supersede the work.
This offline law edit neither enumerates live consumers nor proves re-contracting or activation.
No Concord workflow contract was created here; the manifest does not invent legislative workflow provenance.

## Verification

Required witnesses cover active-session restart, conflict fences, distinct exact pins, stale required refusal, and optional nonblocking reliance.
They also cover terminal read-only reuse, publication without deletion, optional-pin protection, bounded dry runs, deletion atomicity, and crash/replay.
All terminal routes, reopen, owner removal, rebuild, upgrade, restore, authorization denial, and pin/retire races require evidence.
Deleted text must remain absent from Git, retained events, historical pack indexes, and knowledge search.
Architecture-spike decision requirements and CD-0041 Initiative entry, narrative, and scope witnesses remain mandatory.
`internal/store.TestResearchRetirementActivePinsProtectAndTerminalReleaseFences` is a candidate anchor for pin protection, not all these obligations.

Candidate anchors include `internal/store.TestResearchRetirementActivePinsProtectAndTerminalReleaseFences`,
`internal/store.TestResearchRetirementCascadesContent`, and
`internal/agent.TestResearchRetireDryRunAndReplay`.
These source anchors are not a claim that this law-only session ran them or that they cover every obligation.
The coverage shard remains unmeasured until the parent supplies integrated conformance evidence.
