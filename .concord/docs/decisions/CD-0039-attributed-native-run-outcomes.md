# CD-0039: Native-run outcomes are attributed reports, not Concord findings

- **Status:** Accepted
- **Date:** 2026-08-17
- **Scope:** Ops-runbook workflow actions, native-run evidence, partial outcomes; issue #174
- **Approval:** Operator selected attributed outcome records on 2026-08-17
- **Related:** CD-0006, CD-0013, CD-0036 D4, TS1 AJ8, TS4
  ([`agent-mutation-tool-contract.md`](../agent-mutation-tool-contract.md)), TS5
  ([`agent-call-context-contract.md`](../agent-call-context-contract.md)), TS7
  ([`agent-result-envelope.md`](../agent-result-envelope.md)), TS8
  ([`agent-tool-surface-evolution.md`](../agent-tool-surface-evolution.md)),
  [`workflow-engine-contract.md`](../workflow-engine-contract.md)

## Context

The built-in ops runbook declares `start_run`, `checkpoint_run`, `record_health`,
`rollback_run`, and `cleanup_run`. `start_run` and `rollback_run` are external-
effect actions; `record_health` crosses authority. The workflow engine fences the actions and
records generic completion, but it discards the native result. Concord can say
that an action call completed and cannot say what the reporting authority
observed, whether rollback succeeded, or which native run the report concerns.

TS1 `AJ8-health-failure-rollback` requires a failed health observation, a
rollback result, durable `rolled_back` state, and a `partial` outcome. This is
not permission to build a provider executor. TS4 remains controlling: the native
authority performs the operation; Concord records intent, authority, and
evidence.

## Decision

### D1. Concord records an attributed report

A native-run record means:

> authenticated trusted client X reported status Y about native run R at time T,
> with evidence E.

It does not mean Concord performed, observed, or independently verified Y. Every
read that returns the status also returns the reporting authority, report time,
and evidence identity. Dropping attribution from a display or projection is a
contract defect.

A false report can therefore become durable Concord state. The record remains
honest because it preserves who made the claim; it must never be rendered as an
unqualified fact.

### D2. Reporting authority comes from authentication

`reporting_authority_ref` is derived from the active trusted client's
`client_ref` and its validated grant. It is never accepted from workflow fields,
agent prose, or an `EvidenceRef.authority` string.

The native provider/resource being discussed is the report subject, not the
source of Concord authority. It is named by an opaque, non-secret
`native_subject_ref` and digest in the evidence.

### D3. One typed report records the outcome

The workflow records a typed native-run report, `workflow.native_run_recorded`.
Per CD-0040, every native-run report also embeds the shared external-observation
capture component before this surface is implemented. Its closed domain payload
contains:

```text
run_id                 # stable ID, 1–128 characters
native_subject_ref      # opaque non-secret reference, 1–2048 characters
subject_digest
phase                 # start | health | rollback | cleanup
status
evidence_ref
evidence_digest
asserted_at
reporting_authority_ref
actor_ref
```

`recorded_at` remains the core record time. `asserted_at` is the report's own
time, must be RFC3339, and may not be more than two minutes after `recorded_at`.
The named two-minute native-report skew uses the existing authority clock-skew
bound; a different value requires contract evidence. The record identity
and accepted-input digest provide retry identity. The report is a current row written in one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Phase controls the status vocabulary:

```text
start:     started | failed_to_start
health:    healthy | degraded | failed
rollback:  rolled_back | partially_rolled_back | rollback_failed
cleanup:   cleaned | cleanup_failed
```

The action ID supplies the phase; callers do not choose it independently.
Health reports require an observation reference and digest. Rollback reports
require rollback evidence. Unknown statuses, missing required evidence, or a
reused run ID with different subject/authority/content fail structurally.

### D4. The stored row preserves the claim, not a copied verdict

`workflow_native_runs` keeps the latest phase/status for each
`(work_id, run_id)` as current rows. The row is written in the report's one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). It stores the report identity,
reporting authority, actor, subject, evidence, and both times alongside status.

`native_change.status=rolled_back` in TS1 is a read projection over that row. The
binding must also prove the corresponding reporting authority and evidence are
present, even though the scenario asserts only status.

No second generic evidence authority is created. `workflow.evidence_bound`
keeps its existing completed-operation backing rule. A native-run report may be
used as completion evidence only through the existing explicit evidence-binding
action and its authority checks.

### D5. Ops actions gain strict payload contracts

The built-in ops runbook stops accepting arbitrary fields for these actions.
`start_run`, `record_health`, `rollback_run`, and `cleanup_run` declare exact
fields and phase-specific requirements in the workflow definition and canonical
agent payload schema. `checkpoint_run` remains a resume-boundary record and does
not stand in for a native outcome.

The current core agent path that reduces evidence to a locator is insufficient
for this record. Kind, reporting authority, subject digest, and evidence digest
must reach the core as typed fields and be validated before the event appends.

### D6. Workflow-engine mappings and surface input change together

The workflow-engine action mappings record `workflow.native_run_recorded` for
`record_health`, `rollback_run`, `start_run`, and `cleanup_run`, with their
existing started/completed facts as applicable, and write the
`workflow_native_runs` rows in the same transactions. The canonical workflow
and agent payload schemas, generated clients, docs, and registry definitions
move in one change.

Tightening the current arbitrary workflow-action field bag into required typed
phase payloads is a TS8 major change. There is no compatibility alias.
Old clients regenerate, old/new pairs fail closed at
bootstrap, and pre-amendment records never acquire a native status
retroactively. Existing completed workflow-action operations replay as their
recorded generic result; there are no native reports to infer from them. This
record is the explicit operator acceptance required by TS8.

### D7. Native failure uses the existing workflow-action durable operation

The workflow-action claim already owns idempotency, attempt epochs, and replay.
No provider executor or compaction-style composite operation is added.

When a native report shows that the approved logical operation did not complete
successfully, the action records the attributed report and completes its durable
classification as `partial` or `failed` in the same transaction. The agent
surface returns TS7 `partial` with:

- the same workflow-action operation ID;
- the workflow actions that actually completed;
- the native failure and rollback result;
- `effect_state=partial`; and
- `operation_conflict` with `reconcile_operation`.

For a health failure followed by a successful rollback, `start_run`,
`record_health`, and `rollback_run` are completed native steps, but the approved
production change is not successful. `partial` describes that logical result;
it does not claim rollback was incomplete.

An identical retry returns the same durable operation and attributed records.
A changed payload under the same idempotency key receives
`idempotency_conflict`. Attempt epochs fence stale reports.

### D8. `ok` is reserved for successful native predicates

Returning `ok` while the recorded row says `failed` or `rolled_back` is forbidden.
The adapter cannot synthesize `partial` from prose or provider output; the core
derives it from the typed report committed with the durable operation.

A workflow contract whose final predicate is not proved refuses completion with
`missing_evidence`. Native-run partials use
`operation_conflict` and reconciliation so the two error meanings do not drift.

### D9. The execution boundary does not move

Concord does not call a routing provider, run health probes, wait on timers, or
perform rollback. Context cancellation does not prove that any native effect
stopped. The native authority supplies the report and evidence; Concord records it.

## Rejected alternatives

**Reference only.** Concord could not answer the accepted status assertion
without an unbuilt resolver. A typed attributed report states no more than the
reporter claimed and is useful without live provider access.

**Mandatory signed host assertion per report.** Trusted-client registration and
grant validation already authenticate the reporter. Per-run host signing adds
launcher/adapter infrastructure without changing the claim Concord makes. The
record shape leaves room to bind a signature later.

**Caller-supplied `native_authority`.** A string in agent input cannot confer
authority.

**Return `ok` and let workflow state explain recovery.** This would hide an
incomplete logical operation and contradict the accepted TS1 outcome.

**Core-owned composite executor.** This would make Concord perform apply, health,
and rollback, violating TS4 and the native-authority boundary.

**Reuse checkpoints as run status.** A resume boundary is not evidence authority
and carries no phase/status invariants.

## Verification

- `AJ8-health-failure-rollback` binds with no adapter-domain inference.
- The record and every read carry reporter, subject, status, evidence, and time.
- Agent-supplied authority names cannot change attribution.
- Phase/status/evidence mismatches fail before the row is written.
- The implementing change binds the current-state anchor: native-run rows read
  back unchanged, with no event-log rebuild (CD-0242 D2).
- Same-key replay returns the same partial operation and no duplicate report.
- A successful predicate returns `ok`; failed health plus successful rollback
  returns `partial` and durable `rolled_back` state.
- No test or production path calls a provider, probes health, or fabricates
  rollback from elapsed time.
