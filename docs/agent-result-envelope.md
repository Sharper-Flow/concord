# Concord Agent Result, Error, Evidence, and Pagination Envelope (TS7)

> **Status:** **Accepted — binding until superseded; producer-bounded result law amended by issue #43.**
> **Accepted by operator:** 2026-08-06.
> **Amendment approved by operator:** 2026-08-11 under issue #43.
> **Decision:** TS7; binding input to CD-0005 §7 and TS8–TS9.
> **Canonical schema:** [`agent-tool-envelope.schema.json`](../contracts/agent-tool-envelope.schema.json).
> **Binding inputs:** PM1 universal query envelope/corpus and accepted TS1–TS6
> job/tool/context/transport contracts.
> **Does not decide:** operation-specific payload fields already owned by TS3/TS4,
> surface identity/change evidence (TS8), measurement evidence (TS9), UI rendering,
> C14, or C15.

## Context

Every tool call needs one shared result law. The binding inputs are PM1's
universal query envelope and corpus and the accepted TS1 through TS6
contracts. This record fixes the strict JSON envelope with exactly one
outcome per call, its universal fields, the durable outcomes, the typed error
contract, authority and freshness semantics, evidence locators, pagination
and output bounds, and adapter transport failures. The producer-bounded
result law is amended by issue #43.
## Contract

The binding contract is sections 1 through 9: the four-outcome decision,
universal fields, the `ok` payload forms, durable `pending` and `partial`
outcomes, the closed error contract with recovery actions, authority and
freshness semantics, evidence reference locators, pagination and output
bounds with the producer-side completeness obligation, and adapter transport
failures. Sections 10 through 12 record scenario requirements, rejected
alternatives, and falsifiers, and carry no obligation.
## 1. Decision

Every Concord tool returns one strict JSON envelope with exactly one outcome:

| Outcome | Meaning |
|---|---|
| `ok` | The requested logical operation completed. For mutations, all claimed effects are authoritative and attributable. |
| `pending` | One durable typed operation exists and has not reached a final state. This is not success. |
| `partial` | At least one cross-authority step occurred, but the logical operation did not complete. Completed/failed steps and recovery are explicit. This is not success. |
| `error` | The request did not produce a successful logical result. Retry and recovery semantics are typed. |

No `success` boolean exists. A committed-but-incomplete operation cannot hide under
`ok`; a transport timeout cannot imply failure if the core has a durable pending or
partial operation; and prose never owns result classification.

## 2. Universal fields

Every outcome carries:

| Field | Contract |
|---|---|
| `schema_version` | Envelope schema version. |
| `manifest_digest` | Exact generated current-manifest digest bound to this client, session, invocation, and result. |
| `adapter_contract_version` | Required only for `origin=adapter`: adapter envelope schema used to encode a pre-core transport/bootstrap failure; it never claims a core surface identity. |
| `request_id` | This transport attempt; audit only, never idempotency. |
| `origin` | `core` for a schema-valid core response; `adapter` only for fail-closed transport errors when no core envelope exists. |
| `tool`, `operation` | One accepted TS3/TS4 pair. |
| `outcome` | `ok|pending|partial|error`. |
| `resolved_scope` | Product/Project/work IDs actually used, or `null` when resolution failed. |
| `authority` | `authoritative|degraded|unreachable`. Degraded may accompany bounded useful data; unreachable cannot masquerade as data. |
| `freshness` | Observation time, `age` in milliseconds, and stale flag, or `null` when no source was reached. The field name preserves PM1 compatibility. |
| `source_version_watermark` | Ordered source identities/versions proving the answer or effect. |
| `ordering_keys` | Declared ordering keys for returned collections; empty for unordered/singular results. |
| `next_cursor` | Opaque continuation token or `null`; always present, so absent never means silently truncated. |
| `omissions` | Typed known omissions; empty when complete. |
| `warnings` | Typed non-fatal warnings; empty when none. |
| `evidence_refs` | Bounded typed locators to evidence authorities, never embedded process exhaust. |
| `replayed` | `true` only when TS5 idempotency returned a prior mutation result/durable operation. |

Read outcomes also carry PM1 `query_id`. Operation-specific schemas validate the
payload nested under exactly one of `items` (collection) or `result` (singular).
The envelope schema validates their placement; the canonical registry validates
their domain fields.

## 3. `ok` outcome

`ok` contains exactly one payload form:

- `items`: bounded collection page; or
- `result`: singular/aggregate operation result.

Mutation success additionally returns:

- `changed_refs`: stable entity kind/ID/new version only—not a copied full state;
- `next_valid_intents`: bounded typed tool/operation choices currently valid; and
- evidence/approval references required to establish the result.

Routine success must be enough to choose the next action. An agent must not call a
separate status/show tool merely to learn the resulting version, canonical reference,
or next valid intent.

## 4. Durable outcomes

### `pending`

Returns one `operation_ref` with stable operation ID, kind, version, current state,
current step, update time, and safe `next_action`. The reference is the TS4 recovery
authority, not a generic queue ID.

### `partial`

Returns:

- the same versioned `operation_ref`;
- ordered `completed_steps`;
- optional `failed_step`;
- typed `error`; and
- one structural `recovery_action`.

`partial` never includes `success`, claims a canonical locator before proof, or
starts a second operation on retry. Reconciliation uses the same operation ID,
expected operation version, and idempotency identity.

## 5. Error contract

Every error contains:

```text
kind
retry_safe
recovery_action
effect_state         # none | possible; `partial` outcomes require partial
message?             # human aid only; agents branch on kind/action
current_versions[]?
candidates[]?
violations[]?
options[]?           # operator resolutions; governing-law conflicts only (CD-0035)
details?             # closed scalar/list values; bounded, no nested dump
adapter_reason?      # required closed reason when origin=adapter
```

`recovery_action` names a closed strategy and required references:

```text
none
retry_same_request
refresh_context
reread_entities
request_approval
provide_evidence
reduce_limit
use_next_cursor
restart_query
adjust_budget
reconcile_operation
resolve_ambiguity
contact_operator
```

Closed error kinds:

| Family | Kinds |
|---|---|
| Scope/auth | `unknown_scope`, `ambiguous_scope`, `stale_context`, `unauthorized`, `approval_required`, `approval_invalid` |
| Concurrency/retry | `version_conflict`, `idempotency_conflict`, `operation_conflict` |
| Domain | `invalid_transition`, `invalid_relation`, `invariant_violation`, `missing_evidence`, `not_terminal`, `outcome_mismatch` |
| Authority/freshness | `stale_requires_review`, `degraded_not_allowed`, `unreachable` |
| Bounds/input | `invalid_cursor`, `limit_exceeded`, `budget_refused`, `invalid_input` |
| Execution/transport | `cancelled`, `timeout`, `transport_failure`, `malformed_response`, `internal_error` |

Expected domain outcomes such as `not_compacted`, canonical-note `missing`, or
canonical-note `ambiguous` remain typed `ok.result` variants when the query itself
succeeded. They are not infrastructure errors.

`approval_required` refusals that minted a core approval challenge carry the
CD-0037 typed `consequence_summary` object (tool, operation, consequence,
operation digest, canonical scope and version bindings, expiry). It is derived
only from the facts bound to the challenge; challenge-less approval refusals
carry no summary, and a summary without a challenge is invalid. The adapter
transports the object unchanged; the host renders it for the operator.

`retry_safe=true` means repeating the **same canonical request with the same
idempotency key** cannot create another effect. It does not mean retrying immediately
will succeed; `recovery_action` owns preparation.

Error kind structurally constrains recovery: ambiguity resolves candidates; stale
context refreshes; version/domain conflicts reread entities; approval/evidence errors
request their missing proof; limit errors reduce the limit; budget refusal adjusts
the budget; invalid cursors restart the query; operation conflicts reconcile;
unreachable/unauthorized/internal failures contact the operator or use their
explicitly allowed same-request retry. Cancellation/timeout may
use an `error` outcome only when `effect_state=none`; possible effects become
`operation_conflict` with reconciliation.

`options` is the one affordance that names choices for the operator rather than
recovery steps for the agent. It is permitted only on a governing-law conflict —
`invariant_violation` with `contact_operator` recovery — and its closed vocabulary
is `clarify`, `amend_contract`, `accept_scope_cut`. It is permitted rather than
required, so an `invariant_violation` that offers no choice stays valid. A refusal
carrying `accept_scope_cut` also carries the `approval_ref` that option resolves
against: the choice is actionable, not advisory. See
[CD-0035](./decisions/CD-0035-governing-requirements-at-capture.md).

## 6. Authority, freshness, and omissions

- `authoritative`: every required source watermark proves completeness/currentness.
- `degraded`: useful bounded result exists but named sources/items are missing or
  lagging; `omissions` is non-empty.
- `unreachable`: required authority could not be queried and no data payload is
  returned.

An authoritative empty result is `ok` with empty `items`, not `error`. A degraded
empty result is still degraded and names omissions; it cannot become authoritative
empty. Stale data follows the operation's accepted policy: explicit degraded result,
`stale_requires_review`, or blocked execution—never silent fallback.

Warnings do not weaken authority. If a condition changes completeness/currentness,
it belongs in authority/omissions, not a warning string.

## 7. Evidence references

Evidence is returned as bounded typed locators:

```text
kind
authority
locator_kind
locator
version?
digest?
```

Allowed kinds include verification, review, approval, commit, durable note, native
run, and artifact. Ordinary external references are opaque stable locators. Canonical
git notes retain PM6 commit/content hashes because those prove durable knowledge.

The envelope never embeds raw sub-agent transcripts, screenshots, logs, binary
bytes, full git documents, credentials, client keys, or approval secrets. Agents
retrieve bounded evidence from its authority only when needed.

## 8. Pagination and output bounds

- Summary collections: default 20, requested maximum 100.
- `detail=full` collections: maximum 20.
- Snapshot previews: default 5, maximum 20 per requested bucket; duplicate work
  across buckets is emitted once with view flags.
- Relation graphs: depth maximum 3, 100 nodes, 200 edges.
- Serialized envelope: hard maximum 65,536 bytes.
- Warnings: 16; omissions: 16; evidence refs: 32; changed refs: 32; next valid
  intents: 16; error candidates/violations: 20 each; error options: 3.

If the requested page would cross any bound, the core returns fewer complete items
plus a non-null cursor and explicit omission/continuation metadata. It never cuts an
item, emits malformed JSON, silently truncates, or substitutes an unbounded file.

The opaque authenticated cursor binds tool, operation, resolved scope, filters,
detail, ordering, source watermark, and last emitted key. Cursor tampering, wrong
operation/scope/filter reuse, or invalidated source version returns `invalid_cursor`.
No server session is required.

### 8.1 Producer-side completeness obligation

Every delegated or mutation-result producer validates the exact tool/operation
payload against its closed generated output schema before exchange. It also
checks the 65,536-byte serialized envelope cap, caller `max_bytes` and
`max_items` budgets, and the envelope's completeness markers before returning
`ok`. A producer refuses an over-budget result; it does not silently cut an
item, array, cursor, omission, or evidence reference.

One repair attempt is permitted only for a stored result carrying a recognized,
durable legacy contract version with an existing migration path. Fresh current
schema defects fail closed. Repair never re-executes an effect and never falls
back to generic or guessed output. Persistent producer failure uses the existing
closed `malformed_response`/`invalid_input`, `limit_exceeded`, or
`budget_refused` kinds and their typed recovery actions.

Consumers reject malformed or oversized structured output. They never replace a
rejected result with a head, tail, middle, or other excerpt; legitimate partial
coverage uses only the existing `partial`, `omissions`, pagination, and
`evidence_refs` mechanisms.

## 9. Adapter transport failures

When no valid core envelope is available, TS6's adapter may construct only a minimal
TS7 `error` envelope with `origin=adapter`, `authority=unreachable`, and one of:

- `transport_failure` — CLI missing/spawn/I/O failure;
- `malformed_response` — stdout is not exactly one schema-valid envelope;
- `timeout` — caller budget ended with no core durable outcome; or
- `cancelled` — cancellation completed before any authoritative effect.

Adapter errors also carry closed `adapter_reason`:
`missing_binary|spawn_failure|io_failure|malformed_core_response|timeout_no_effect|cancelled_no_effect|manifest_mismatch|grant_bootstrap_failed|unknown_effect`.
The adapter's own `adapter_contract_version` makes this envelope schema-valid before core
invocation; `manifest_mismatch` fails closed and recovers by regenerating the current
manifest and restarting the session, never by parsing arbitrary details.

If an effect may have occurred, adapter returns `operation_conflict`/recovery to
re-read or reconcile; it never guesses no effect. Raw stderr is bounded diagnostic
evidence, not parsed domain state.

## 10. Scenario and current-surface requirements

TS7 must pass:

- every PM1 envelope/error assertion;
- every TS1 final-state/communication/prohibited-effect scenario;
- all TS5 stale/version/approval/idempotency variants;
- mutation success without a routine follow-up status call;
- pending/partial compaction recovery with one versioned operation;
- authoritative empty vs degraded empty vs unreachable;
- cursor continuation/tampering/cross-operation misuse;
- maximum-byte/page/graph/warning/evidence bounds; and
- malformed stdout, timeout, cancellation, and unknown error-kind rejection.

Unknown outcome/error/recovery discriminants fail schema validation. TS8 owns exact
manifest-digest identity and change evidence; adapters do not accept unknown
variants as generic prose.

## 11. Rejected alternatives

- `success: true|false` with nested errors.
- HTTP-status-only or natural-language-only failures.
- One giant result object with optional fields for every outcome.
- Silent truncation or absent cursor meaning "maybe more."
- Full state after every mutation.
- Routine mutation success requiring another status/show call.
- Generic queue/job IDs without typed version/current step/recovery.
- Inline raw logs, transcripts, screenshots, documents, credentials, or process
  exhaust.
- Adapter-defined error semantics that diverge from core.

## 12. Falsifiers

Reopen TS7 when:

- supported agents cannot recover from a typed error without parsing `message`;
- routine successful mutation still requires a follow-up read to choose next action;
- legitimate PM1/TS1 payloads cannot fit bounded pagination;
- the 65,536-byte cap regularly forces pathological extra calls;
- cursor binding prevents legitimate continuation after accepted source changes;
- evidence locators cannot identify required proof without embedding raw content;
- adapters/clients cannot validate the canonical schema consistently; or
- a new accepted operation requires a genuinely distinct outcome/recovery class.

Any new outcome, error kind, or recovery action requires a named scenario and TS8
compatibility treatment. Prose convenience is not sufficient evidence.

## Acceptance criteria

- Given any envelope
  When it is validated
  Then unknown outcome, error, or recovery discriminants are rejected, and no
  `success` boolean exists anywhere.

- Given a degraded or unreachable authority
  When a result returns
  Then degraded carries named omissions and unreachable returns no data
  payload; neither masquerades as authoritative.

- Given a requested page that would cross any output bound
  When the core responds
  Then it returns fewer complete items plus a non-null cursor, and a producer
  refuses an over-budget result rather than silently cutting content.

- Given a cursor from one operation
  When another operation, scope, or tampering attempt consumes it
  Then the core returns `invalid_cursor`.

- Given a committed-but-incomplete cross-authority operation
  When the caller inspects it
  Then the outcome is `pending` or `partial` with a versioned operation
  reference and recovery action, never `ok`.

## Verification

The envelope is a per-call property below scenario grain, so every criterion
carries a typed exemption in the record naming the envelope test that proves
the guarantee.

- Criterion 1 is proved by `TestEnvelopeRejectsUnknownVariantsAndFields` and
  `TestEnvelopeRejectsUnknownFieldsAcrossEveryOutcome`
  (`internal/agent/envelope_test.go`).
- Criterion 2 is proved by `TestEnvelopeGoldenOutcomes`
  (`internal/agent/envelope_test.go`).
- Criterion 3 is proved by `TestEnvelopeHasHardSerializedLimit`,
  `TestMutationResultProducerAcceptsCanonicalPayload`
  (`internal/agent/envelope_test.go`), and `TestBudgetFieldsRefuseOrBoundResultsStructurally`
  (`internal/agent/runtime_test.go`).
- Criterion 4 is proved by `TestAuthenticatedCursorBindsOperationAndRejectsTampering`
  (`internal/agent/runtime_test.go`).
- Criterion 5 is proved by `TestOutcomeMismatchIsClosedAndCannotDowngrade`
  (`internal/agent/envelope_test.go`) and the bound `AJ6-partial-publication`
  scenario of `scenarios/agent-jobs.v1.json`, executed by
  `TestAgentJobsCorpus` (`internal/agent/agent_jobs_corpus_test.go`).
  Section 12 records the falsifiers for each guarantee.
