# Concord Agent Result, Error, Evidence, and Pagination Envelope (TS7)

> **Status:** **Accepted.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS7; binding input to CD-0005 §7 and TS8–TS9.
> **Canonical schema:** [`agent-tool-envelope.schema.json`](../../contracts/agent-tool-envelope.schema.json).
> **Binding inputs:** PM1 universal query envelope/corpus and accepted TS1–TS6
> job/tool/context/transport contracts.
> **Does not decide:** operation-specific payload fields already owned by TS3/TS4,
> surface identity/change evidence (TS8), measurement evidence (TS9), or UI rendering.

## Context

Every tool call needs one shared result law. The binding inputs are PM1's
universal query envelope and corpus and the accepted TS1 through TS6
contracts. This record fixes the strict JSON envelope with exactly one
outcome per call, its universal fields, the durable outcomes, the typed error
contract, authority and freshness semantics, evidence locators, pagination
and output bounds, and adapter transport failures.
## Contract

Recovery reconciles current state and names a caller action; no replay exists
([CD-0242](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2 and D12).

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
| `origin` | `core` for a schema-valid core response. `adapter` only when no core envelope exists, for a fail-closed transport error or a pre-dispatch input refusal with no effect. |
| `tool`, `operation` | One accepted TS3/TS4 pair. A pre-dispatch wrapper refusal uses an empty `operation` if no accepted pair can be resolved. |
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
or next valid intent. The oversized-result transport in [§8.2](#82-lossless-oversized-result-transport)
is the exception.

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
correct_request
retry_same_request
refresh_context
reread_entities
request_approval
provide_evidence
reduce_limit
use_declared_route
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
| Concurrency/retry | `version_conflict`, `idempotency_conflict`, `operation_conflict`, `resource_busy` |
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
request their missing proof; limit errors reduce the limit where the operation
exposes one, or name an executable recovery for that operation under
[§8.2](#82-lossless-oversized-result-transport); budget refusal adjusts
the budget; invalid cursors restart the query; operation conflicts reconcile.
An adapter-origin `session_vacate` error with reason `unknown_effect` and
effect `possible` reports the possible effect with a working recovery, per
[CD-0190 D3](./decisions/CD-0190-cd-0179-d1-and-d4-release-at-the-verified-vacate-landing.md#d3-every-refusal-after-the-committed-request-reports-a-possible-effect-with-a-working-recovery).
`session_vacate` reconciles against current stored state ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). [CD-0242 D12](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d12-every-refusal-states-an-action-the-caller-may-take)
requires the refusal to state that action: `retry_same_request`, or
`contact_operator` when reconciliation cannot run. Core-origin errors and
other operations keep reconciliation.
Unreachable and internal failures contact the operator or use their
explicitly allowed same-request retry. An unauthorized refusal contacts the
operator or names a declared route: `required_refs` lists the workflow
actions in execution order. Cancellation/timeout may
use an `error` outcome only when `effect_state=none`; possible effects become
`operation_conflict` with the recovery this section permits.

`options` is the one affordance that names choices for the operator rather than
recovery steps for the agent. It is permitted only on a governing-law conflict —
`invariant_violation` with `contact_operator` recovery — and its closed vocabulary
is `clarify`, `amend_contract`, `accept_scope_cut`. It is permitted rather than
required, so an `invariant_violation` that offers no choice stays valid. A refusal
carrying `accept_scope_cut` also carries the `approval_ref` that option resolves
against: the choice is actionable, not advisory. See
[CD-0035](./decisions/CD-0035-governing-requirements-at-capture.md).

### 5.1 Contract-request validation guidance

A pre-effect contract-request input refusal identifies the relevant failing field and constraint. Its message includes the accepted shape and a minimal illustrative example derived from that field's effective canonical schema. The diagnostic fits within the existing message and envelope bounds without cutting the field, constraint, shape, or example.

Validation still evaluates every required schema branch. Diagnostic selection does not change which requests pass, including oneOf exclusivity. A branch contradicted by a supplied discriminator or closed fieldset does not contribute an unqualified field error when another branch accepts that discriminator or fieldset. When no single branch applies, the message labels the alternatives rather than treating one branch's local restriction as a rule for the whole request.

The same schema owns authoring guidance and refusal guidance. An example establishes syntax only; it grants no authority, evidence, entity existence, or workflow admission. Error kind, retry safety, recovery action, effect state, approval requirements, and all existing bounds remain unchanged.

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
- Serialized envelope: hard maximum 51,200 bytes.
- Warnings: 16; omissions: 16; evidence refs: 32; changed refs: 32; next valid
  intents: 16; error candidates/violations: 20 each; error options: 3.

If the requested page would cross any bound, the core returns fewer complete items
plus a non-null cursor and explicit omission/continuation metadata. It never cuts an
item except through the lossless transport in [§8.2](#82-lossless-oversized-result-transport),
emits malformed JSON, silently truncates, or substitutes an unbounded file.

The opaque authenticated cursor binds tool, operation, resolved scope, filters,
detail, ordering, source watermark, and last emitted key. Cursor tampering, wrong
operation/scope/filter reuse, or invalidated source version returns `invalid_cursor`.
No server session is required.

### 8.1 Producer-side completeness obligation

The OpenCode adapter encodes each raw Concord envelope as one `ToolResult` object:
`{title, output, metadata}`. The `output` field contains exactly one serialized
envelope. OpenCode truncates tool output at 2,000 lines or 51,200 bytes, so the
Concord envelope cap matches the pinned host transport limit.

Every delegated or mutation-result producer validates the exact tool/operation
payload against its closed generated output schema before exchange. It also
checks the 51,200-byte serialized envelope cap, per-operation pagination and
cardinality bounds, and the envelope's completeness markers before returning
`ok`. A producer refuses an over-budget result unless it uses the bounded
lossless transport in [§8.2](#82-lossless-oversized-result-transport); it does not
silently cut an item, array, cursor, omission, or evidence reference.

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

### 8.2 Lossless oversized-result transport

Mutation results and full work reads carry complete WorkPins inline whenever the final serialized envelope fits the unchanged 51,200-byte limit. When it does not fit, the producer may return a closed projection reference instead of the oversized content. The reference identifies the affected work items and versions, the source operation, the exact document digest and byte length, and the declared read route that returns that document as lossless authenticated pages.

For a mutation, the complete document is computed inside the mutation transaction and retained atomically with the write. Replay and projection reads refer to that same immutable post-state document, not a later WorkPin. Changed references and truthful effect metadata remain in the mutation envelope. An unavailable or unretainable document refuses the mutation before effect.

A projection reference or fragment grants no complete pin, intent, question, or acceptance authority. A consumer reconstructs the complete document, verifies its identity, length and digest, and validates its schema before using that authority. The reconstructed mutation result carries every required WorkPin at its changed-reference version and every intent derived from those pins. This oversized-result transport is the exception to the routine-success rule against a follow-up status read.

A full work-list read may page within an otherwise oversized single item by the same lossless transport. It preserves the complete item and the list continuation, with no missing or duplicated item. Pages bind one captured document and the original query; drift refuses before emitting a fragment from a different document. Existing inline and item-count paging remain unchanged when they fit.

Tests prove both inline and reconstructed results meet the same completeness, version, derivation and schema obligations. No recorded failure, context, evidence or other required content is discarded to fit the envelope. A size refusal names an executable recovery for that operation; it does not prescribe reducing a limit the operation does not expose.

## 9. Adapter failures and local refusals

When no valid core envelope is available, TS6's adapter may construct a minimal
TS7 `error` envelope with `origin=adapter` and `authority=unreachable`.
Freshness is null and the source watermark is empty. Adapter origin includes
transport failures, pre-dispatch input refusals, lane authorization refusals,
and worker-recovery failures. It never claims core authority.

The [canonical envelope schema](../../contracts/agent-tool-envelope.schema.json)
owns the machine vocabulary. [Section 5](#5-error-contract) constrains recovery for each kind.
Adapter errors admit effect `none` or `possible`, never `partial`.
The schema admits these adapter-origin kinds:

| Kind | Meaning | Effect | Recovery |
|---|---|---|---|
| `transport_failure` | Host context, lease admission, binary, spawn, I/O, or bootstrap failure. | `none` | The typed action from section 5; bootstrap mismatch requires `contact_operator`. |
| `malformed_response` | No schema-valid core response exists. | `none`; an uncertain effect follows the rule at the end of this section. | The typed action from section 5; uncertain effects require recovery before retry. |
| `timeout` | The deadline ended with no authoritative effect. | `none` | `retry_same_request` |
| `cancelled` | Cancellation completed with no authoritative effect. | `none` | `retry_same_request` |
| `operation_conflict` | A failed operation needs reconciliation. | `none` or `possible` | `reconcile_operation`; an adapter `session_vacate` error with `unknown_effect` and `possible` reconciles against current state ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)) and states its caller action ([CD-0242 D12](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d12-every-refusal-states-an-action-the-caller-may-take)). |
| `unauthorized` | The adapter refused lane tool authorization before dispatch. | `none` | `contact_operator` |
| `invalid_input` | The host wrapper, tool arguments, or CLI input failed validation before dispatch. | `none`, with `retry_safe=false` | `correct_request` |

If the wrapper refusal cannot resolve an accepted tool/operation pair, `operation` is
the empty string and `query_id` is absent. Only this no-effect wrapper refusal may
carry an empty operation. It never fabricates an accepted operation.

The CLI reserves exit 64 with empty stdout for required-field validation and
strict invoke JSON decode refusals before dispatch. The adapter maps that signal
to `invalid_input` with reason `invalid_cli_input`. It never derives this state
from stderr text. Other exits and nonempty malformed stdout do not prove no effect.
If a previous mutation invocation may have effects, a later exit 64 does not
remove that uncertainty. The adapter returns `operation_conflict` with recovery
`reconcile_operation`, or `retry_same_request` for `session_vacate` under
CD-0190 D3.

Every adapter error carries one closed `adapter_reason`. These reasons describe
the producer's failure and recovery. They do not add schema couplings between
a reason and a kind beyond the input and manifest rules named below.

| Reason | Failure | Effect | Recovery |
|---|---|---|---|
| `missing_binary` | The CLI binary is unavailable. | `none` | `contact_operator` |
| `spawn_failure` | The CLI transport cannot start. | `none` | `contact_operator` |
| `io_failure` | The CLI transport cannot return a read or context result. | `none` | `retry_same_request` or `contact_operator` |
| `malformed_core_response` | The response is not one valid core envelope. | `none` | `retry_same_request` or `contact_operator`, as the typed action states. |
| `timeout_no_effect` | The deadline ended without an effect. | `none` | `retry_same_request` |
| `cancelled_no_effect` | Cancellation ended without an effect. | `none` | `retry_same_request` |
| `manifest_mismatch` | The core and adapter manifests disagree. | `none` | `contact_operator`: regenerate the current manifest and restart the session. |
| `context_resolution_failed` | Host directory or ambient context resolution failed before invocation without a classified cause. | `none` | `contact_operator` |
| `host_lease_missing` | The host lease admission gate refused invocation. | `none` | `contact_operator` |
| `unknown_effect` | A mutation's effect remains uncertain. | `possible` | `reconcile_operation`; an adapter `session_vacate` reconciles against current state ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)), or `contact_operator` when reconciliation cannot run ([CD-0242 D12](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d12-every-refusal-states-an-action-the-caller-may-take)). |
| `session_directory_unreadable` | The host cannot read the session directory. | `none` | `retry_same_request` |
| `worker_abandon_refused` | The recovery operation refused abandonment. | `none` or `possible` | `reconcile_operation` |
| `worker_abandon_receipt_failed` | Abandonment succeeded but its receipt failed. | `possible` | `reconcile_operation` |
| `worker_reconcile_failed_recorded` | Retained-report recovery recorded the original attempt's terminal failure in this call; the attempt is failed and this route cannot recover it. | `possible` | `reconcile_operation` |
| `lane_tool_refusal` | A managed lane called a tool outside its authority. | `none` | `contact_operator` |
| `invalid_cli_input` | The CLI refused input before dispatch. | `none`, with `retry_safe=false` | `correct_request` |
| `invalid_request_wrapper` | The host request wrapper is absent or malformed. | `none`, with `retry_safe=false` | `correct_request` |
| `invalid_tool_input` | Host tool arguments failed validation before dispatch. | `none`, with `retry_safe=false` | `correct_request` |

`manifest_mismatch` requires kind `transport_failure`, effect `none`, and recovery `contact_operator`.
The three input reasons require kind `invalid_input`, effect `none`, recovery `correct_request`, and
`retry_safe=false`. Unknown reasons fail validation. The adapter's own
`adapter_contract_version` validates the bootstrap envelope before core invocation.

`correct_request` admits correction of the named malformed input only, within
unchanged authority and scope. It requires `invalid_input`, effect `none`, and
`retry_safe=false`: unchanged replay is not a correction. It cannot replace an
approval, operator denial, effect reconciliation, or committed mutation.

`worker_reconcile_failed_recorded` reports only a failure whose write the core acknowledged in the same call. A refused or uncertain write keeps `unknown_effect`. Its recovery reconciles the original attempt's failed outcome through the current workflow. The recovery itself reopens no attempt, repeats no retained worker recovery, and starts no replacement worker; a later dispatch follows the current workflow's own admission. The envelope identifies the original work and attempt in `error.details`.

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
- the 51,200-byte cap regularly forces pathological extra calls;
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
  Then it returns fewer complete items plus a non-null cursor or uses the
  lossless oversized-result transport in §8.2, and a producer refuses an
  over-budget result rather than silently cutting content.

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
  (`internal/agentcontract/envelope_test.go`).
- Criterion 2 is proved by `TestEnvelopeGoldenOutcomes`
  (`internal/agentcontract/envelope_test.go`).
- Criterion 3 is proved by `TestEnvelopeHasHardSerializedLimit`
  (`internal/agentcontract/envelope_test.go`),
  `TestMutationResultProducerAcceptsCanonicalPayload`
  (`internal/agent/envelope_test.go`), and `TestBudgetFieldsRefuseOrBoundResultsStructurally`
  (`internal/agent/runtime_test.go`).
- Criterion 4 is proved by `TestAuthenticatedCursorBindsOperationAndRejectsTampering`
  (`internal/agent/runtime_test.go`).
- Criterion 5 is proved by `TestOutcomeMismatchIsClosedAndCannotDowngrade`
  (`internal/agentcontract/envelope_test.go`) and the bound `AJ6-partial-publication`
  scenario of `.concord/scenarios/agent-jobs.v1.json`, executed by
  `TestAgentJobsCorpus` (`internal/agent/agent_jobs_corpus_test.go`).
  Section 12 records the falsifiers for each guarantee.
