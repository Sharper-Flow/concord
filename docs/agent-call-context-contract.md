# Concord Agent Call Context, Authorization, and Idempotency (TS5)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS5; binding input to CD-0005 §5 and TS6–TS9.
> **Binding inputs:** PM2 global local authority, PM5 Product/Project/work
> membership, accepted TS1–TS4 job/tool contracts, CD-0003 short-lived CLI, and
> Concord's ambient-context/native-authority constraints.
> **Does not decide:** transport/adapter implementation (TS6), result/error field
> layout (TS7), manifest identity/change evidence (TS8), measurement evidence (TS9), user-interface
> rendering, C14, or C15.

## 1. Decision

Every Concord call carries one **typed call envelope** beside its tool-specific
domain input. The model does not repeatedly supply routine path, trust, actor, or
session fields. The accepted adapter/host derives them from authenticated client and
repository context; the Go core re-resolves and validates them on every short-lived
invocation.

The envelope contains:

```text
schema_version
request_id
grant_token                 # opaque core-issued capability grant (bearer secret)
client_ref
ambient_project_id
selected_product_id?       # absent while unresolved/ambiguous
scope_version
idempotency_key?           # required for mutation; absent for reads
approval_refs[]            # durable core-issued references, never caller prose
```

Tool-specific inputs continue to carry stable work/Project/Product IDs and expected
entity versions where the requested intent needs them. Filesystem paths, repository
names, display names, URLs, and trust booleans never confer scope or authority.

There is no mutable protocol session and no ambient state held by a daemon. The
envelope is a self-contained scope assertion over PM2's durable authority. A cached
envelope may save resolution work, but the core treats its IDs/version as assertions
to verify—not authority to trust.

## 2. Ambient context resolution

### 2.1 Establishment

At client/session entry, the accepted host resolves the current repository through
its stable Project locator, then asks the core for:

- `ambient_project_id`;
- all owning Product candidates;
- `selected_product_id` when ownership is singular or an operator explicitly
  selects one; and
- `scope_version`, the current Product↔Project membership/version watermark used
  for that resolution.

The model receives the resolved Product/Project summary as context, not a path it
must thread through every call. TS3 `concord_product_view.resolve` remains available
when the agent needs to inspect or change the explicit Product selection.

### 2.2 Ambiguity

If one Project belongs to multiple Products, `selected_product_id` remains absent.
Any Product-scoped operation returns typed `ambiguous_scope` with bounded candidates
until the caller selects a stable Product ID. Project-local identity alone never
silently chooses a Product, and primary membership is not an ambiguity tiebreaker.

### 2.3 Explicit scope

Routine calls use the selected ambient Product. Explicit stable scope is accepted
only when the intent itself requires it:

- resolving ambiguity;
- reading another authorized Product;
- creating or changing explicit multi-Project/cross-Product work membership; or
- acting on a stable work ID whose derived Product scope must be verified.

There is no generic `target_path`, `target_confirmed`, or `trust_remote` argument.
Cross-Product mutations require an explicit complete membership/resulting-scope
intent plus a durable cross-scope approval reference. The core derives Product scope
from PM5 memberships and returns it; callers never store Product copies on work.

## 3. Context freshness

The core compares `scope_version` with current membership authority on every call.

- **Current:** execute normally.
- **Outdated but semantically unchanged:** a read may execute and returns the new
  scope version with explicit `context_refreshed`; a mutation fails `stale_context`
  before any effect.
- **Changed ownership, selected Product no longer valid, or newly ambiguous:** all
  Product-scoped calls fail with typed `stale_context` or `ambiguous_scope` and the
  current bounded candidates/recovery action.
- **Unreachable authority:** fail `unreachable`; never authorize from cached scope.

This gives safe read ergonomics without allowing a stale mutation target. PM1 source
freshness remains separate: a current scope can still point at stale/degraded data,
which TS3/TS7 must report under PM1's authority contract.

## 4. Principal and authorization

### 4.1 Principal source and grant proof

`grant_token` is an opaque, unguessable, core-issued capability grant carried as a bearer
secret. Its authoritative record binds the attributable human, agent, or delegated
automation principal, trusted client, allowed Product/Project scope, capability classes,
issue/expiry, and revocation state. The core resolves `principal_ref` from this grant
and validates it on every invocation. Tool input cannot name or impersonate a
principal, widen scope, or add capabilities. If the transport cannot provide a valid
grant, the call is denied. The non-secret record reference for the same grant is
returned by the `grant` verb as `grant_ref`; supplying it where the token belongs is
refused distinguishably.

`client_ref` identifies the calling integration for audit and must match the grant
binding; it is not independent authority. Grant secrets are
never exposed to the model, logged, or persisted in Product artifacts. Model name,
prompt text, or an agent-asserted role is never a grant.

### 4.2 Core-owned policy

The core authorizes the tuple:

```text
principal × tool operation × resolved Product/Project/work scope × consequence class
```

Operation definitions declare their required capability and approval class in core
code generated from the same canonical contract used by adapters. Plugin prose,
tool descriptions, instructions, and caller booleans cannot weaken it.

The initial capability classes are deliberately small:

| Capability | Permits |
|---|---|
| `product_read` | TS3 reads in authorized Product scope |
| `work_define` | capture/revise non-consequential work intent |
| `work_transition` | lifecycle/workflow actions whose own obligations pass |
| `work_relate` | membership/relation mutations in authorized scope |
| `work_compact` | approved PM6 publication/reconciliation |
| `cross_scope` | explicit mutation spanning outside selected ambient Product |

These are policy capabilities, not roles, assignments, or new agent tools. A
principal receives only the capabilities required by its trusted client/session
grant. Product/Project restrictions are evaluated in addition to capability class.

## 5. Human approval evidence

Approval is a durable core record, referenced by opaque `approval_ref`. It binds:

- approving human principal and trusted client/session evidence;
- exact tool operation and canonical input digest;
- resolved Product/Project/work scope and expected entity versions;
- consequence summary;
- issue time, expiry or explicit non-expiring policy, and allowed use count; and
- revocation/consumption state.

Consequential mutations default to single-use approval. Reuse requires an explicit
bounded approval policy naming the operation family, scope, expiry, and maximum
uses. Changing any bound input, version, scope, content/home, or consequence makes
the approval inapplicable. The core consumes a single-use approval in the same
transaction that records the authorized domain intent; cross-authority execution
then follows the durable operation's recovery contract.

Rejected approval forms:

- `approved: true`, `target_confirmed`, or other self-asserted booleans;
- natural-language approval copied into a tool argument;
- approval inferred from an earlier unrelated action or chat message;
- an adapter-only permission check with no core validation; or
- an approval that survives changed versions/content/scope without revalidation.

## 6. Expected versions

Every mutation of an existing canonical entity carries its expected version.

- `revise_intent`, lifecycle, and workflow actions carry the work version.
- membership replacement carries the work version.
- relation link/unlink carries every affected endpoint version.
- supersede/restore carries predecessor, current successor, and replacement
  successor versions as applicable.
- compaction publish pins the terminal work version plus approved content/home
  digest; reconcile carries the expected durable-operation version for atomic step
  compare-and-swap, or terminal work/version only while discovering an orphan.
- capture has no prior entity version but requires idempotency.

Version mismatch fails before approval consumption or any effect and returns current
version plus safe reread/retry guidance under TS7. The core never silently rebases a
mutation onto newer state.

## 7. Idempotency

### 7.1 Key contract

Every mutation requires a caller-stable `idempotency_key`; reads use only
`request_id`. The trusted orchestrator/client creates the key for one logical intent
and reuses it for every transport retry or recovery of that intent. Randomly
generating a new key on each network attempt is invalid client behavior.

The core first looks up deduplication by the immutable tuple:

```text
principal_ref × tool × operation × idempotency_key
```

This lookup occurs before deriving a new scope for a retry. On first authorization,
the record stores the canonical request digest, immutable authorized-scope snapshot,
accepted event/durable-operation reference, and latest result identity. The request
digest excludes transport-attempt fields (`request_id`, refreshed context version)
but includes all domain inputs, explicit scope selection, expected entity versions,
and approval-bound consequence content.

### 7.2 Replay behavior

- Same key + same canonical request digest: validate the current grant against the
  original authorized scope, then return the original committed result or current
  durable-operation state; create no new effect. Later membership drift cannot make
  the retry miss its original record.
- Same key + different digest: fail `idempotency_conflict`; create no effect.
- Original attempt not committed: a retry may execute normally.
- Original cross-authority attempt partial/pending: return the same durable operation.
  Before another external step, revalidate its pinned scope/version/approval; pause
  for renewed authorization when those no longer apply, never start a second
  publication/external effect.

Idempotency identity remains reconstructable for as long as the referenced domain
event or durable operation remains authoritative. No short TTL may make a valid
retry duplicate a durable effect.

## 8. Request identity and audit

`request_id` is unique per transport attempt and links logs/metrics. It does not
deduplicate. Domain events and durable operations record principal, client,
request ID, idempotency identity, approval reference when applicable, resolved
scope/version, and resulting entity versions.

Sensitive credentials, approval secrets, raw prompt text, and process exhaust are
not copied into Product memory. References identify protected evidence held by its
own authority.

## 9. Scenario requirements

TS5 adds/requires these variants in the TS1 runner:

| Scenario | Passing oracle |
|---|---|
| Singular ambient Project | Product resolves without model-supplied path/scope fields. |
| Shared Project | Mutation blocks with bounded candidates; no Product guessed. |
| Stale unchanged context read | Read succeeds with explicit refreshed context version. |
| Stale context mutation | No effect; current scope/version and recovery returned. |
| Wrong/cross Product | No silent mutation; explicit scope plus valid capability/approval required. |
| Missing/spoofed principal | Mutation denied; caller cannot self-assign identity. |
| Missing/changed approval | Consequence blocked; approval remains unconsumed on version conflict. |
| Duplicate delivery | Same key/digest returns same result/effect count; changed digest conflicts. |
| Partial compaction retry | Same durable operation resumes; no second note/effect. |

## 10. Rejected alternatives

- Repeating filesystem target paths and trust booleans in every tool schema.
- CWD/repository name as unverified authority.
- Mutable server-session context or adapter cache as the only scope truth.
- Product IDs copied onto work instead of PM5-derived scope.
- Silent context refresh for mutations.
- Role strings or model identity supplied by the agent.
- Plugin-only authorization or approval.
- Approval text/boolean instead of an operation-bound durable reference.
- Request IDs used as idempotency keys.
- Expiring deduplication before the underlying durable effect can no longer replay.
- Silent last-write-wins on version conflict.

## 11. Falsifiers and amendment rule

Reopen TS5 when:

- the accepted primary client cannot inject an authenticated envelope without
  exposing routine plumbing to the model;
- context resolution cannot remain correct across shared/cross-Product Projects;
- Product-wide default grants prove too broad for least-privilege operation;
- unchanged stale-read refresh causes a demonstrated authority error;
- approval binding cannot survive the selected transport without trusting prose;
- idempotency records become the dominant retained-state cost; or
- a supported non-OpenCode client requires a different principal/context proof that
  cannot map to this logical envelope.

Any amendment must preserve core-owned authorization, explicit cross-scope intent,
expected-version conflict detection, operation-bound approval, and durable retry
identity.
