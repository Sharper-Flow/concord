# CD-0040: External observations prove presence; absence requires pinned completeness

- **Status:** Accepted
- **Date:** 2026-08-17
- **Scope:** External-state provenance, verification, scope completeness, and consumption; issue #89
- **Approval:** Operator approved the carrier, probe, freshness, divergence, consumption, absence, decision-vehicle, and writer choices on 2026-08-17
- **Related:** CD-0008 D2/D3/D5, CD-0014, CD-0022, CD-0028, CD-0030,
  CD-0036 D3/D4, CD-0039, issues #87, #88, #174, TS3
  ([`agent-read-tool-contract.md`](../agent-read-tool-contract.md)), TS7
  ([`agent-result-envelope.md`](../agent-result-envelope.md))
- **Amends:** CD-0039 native-run records before their implementation
- **Supersedes:** nothing

## Context

Concord records facts about state it does not own: Git positions, worktrees,
external conditions, provider/native outcomes, recovery artifacts, and
inventories. A record may be internally valid and wrong about the present. Issue
#89 was prompted by three versions of that failure: a stale recovery artifact
would have restored superseded services, the same artifact omitted part of its
population from birth, and a plausible checkout had been replaced by an old
snapshot before an audit trusted it.

No background poller may keep these records fresh. CD-0014 and the accepted
no-polling rule require verification at an explicit read, decision, or mutation
boundary. CD-0008 already says unreadable input contributes `unknown`, not
absence, and negative or safety conclusions fail closed inside their bounded
closure. This record makes that law concrete for external observations.

Public mechanisms point in the same direction. Git trees can prove absence
inside one pinned tree. HTTP preconditions and Kubernetes resource versions bind
reads and writes to an external version. Paginated APIs distinguish a stable
snapshot, traversal completion, and advisory counts. SLSA and in-toto digests
prove what was named, not that the producer named the whole world. A digest over
12 services proves those 12 records; it does not prove there were only 12.

## Decision

### D1. Provenance is shared; domain truth remains local

External-state records embed one closed provenance and verification component.
The component supplies common identity, observed-universe, verification, and
freshness semantics. Purpose-built domain events and projections retain their
own status vocabularies and invariants. Native-run phases, Git proof, external
conditions, and backup validation do not collapse into one generic status table.

A bounded generic fallback exists for observations that have no owning domain,
such as one-off recovery artifacts or environment descriptions. It uses closed
subject/capture vocabularies and cannot create workflow authority. It is not an
arbitrary external-state table.

### D2. Applicability and examined universe are different axes

CD-0022's `home|explicit` scope tuple answers where a finding or record applies.
It does not answer what an observation examined. An external observation carries
both when applicability matters:

- **applicability scope:** Product/Project/component/tag reach;
- **observed universe:** exact subjects, query/filter/shard/authorization scope,
  snapshot anchor, and traversal coverage.

Neither substitutes for the other. Reusing one field for both is invalid.

### D3. Every capture carries immutable provenance

The shared component contains at least:

```text
observation_id
subject_kind
subject_ref
capture_method
captured_at
reporting_authority_ref
subject_digest?
observed_universe
freshness_policy_ref
divergence_policy_ref
```

`reporting_authority_ref` is derived from the authenticated trusted client or a
core-owned Git probe. It is never accepted from agent prose. Subject references
are opaque, bounded, and non-secret. They remain evidence subject data until an
owning registry such as C15 exists; this record does not make C15 a floor
prerequisite.

Capture provenance is append-only. Later checks append verification events; they
do not edit the original observation or turn an attributed report into a Concord
finding.

### D4. Examined scope separates shape, coverage, and anchor

`observed_universe` is a closed structure:

```text
shape                 # item | collection | stream
applied_scope         # exact effective path/query/filter/shard/auth scope
anchor?               # snapshot_token | structure_digest
coverage              # complete | partial | unknown
observed_count?
observed_refs[]         # bounded identities or a domain-owned set reference
total                  # eq(value) | gte(value) | unknown
completion_evidence?  # authoritative_item_read | end_signal |
                       # closed_structure_digest | exhaustive_local
canonical_identity_key
omissions[]
```

These fields are orthogonal. `collection` does not mean complete. A finite list
may be truncated. A total may be an estimate. A set digest proves the identity
of the captured set, not the scope of the world the producer chose to capture.

For a large domain-owned collection, the component may point at that domain's
bounded projection rather than copy every identity into the event. The reference,
distinct count, canonical identity key, and set digest must still bind the same
captured set.

The following combinations are invalid:

- `complete` without an exact `applied_scope`, stable anchor, completion witness,
  canonical identities, and zero unresolved omissions;
- an end-of-list witness without a snapshot token that stayed constant through
  the traversal;
- a closed-structure witness without the digest that closes that structure;
- `total=eq(n)` when the distinct canonical identity count is not `n`;
- a stream claiming complete coverage of the world; or
- an unanchored observation claiming authoritative absence.

Provider counts and remaining-item estimates may detect an obvious gap but do
not prove completeness by themselves.

### D5. Presence is broad; absence is earned

The governing rule is:

> An enumeration proves presence only. Absence requires a completeness witness
> over a pinned universe, checked at read time.

An item is authoritatively absent only after an authoritative item read over the
exact subject/anchor returns a typed negative result. An item is absent from a
collection only when D4 permits `coverage=complete` and its canonical identity is
not in the captured set.

Every other case returns `not_observed`, never `absent`. `partial`, `unknown`, an
expired anchor, unresolved aliases, failed pages, filtered authorization, or any
omission prevents a negative conclusion. Open APIs that expose no stable anchor
can still provide useful presence evidence; they cannot provide authoritative
absence.

### D6. Verification is append-only and attributed

Verification events bind one observation to:

```text
verification_method
verified_at
verifying_authority_ref
result                 # matched | diverged | unreachable | unavailable
current_anchor?
current_digest?
omissions[]
```

The fold derives one of:

```text
unverified
verified
diverged_expected
diverged_unexpected
unverifiable
```

`unreachable` is a failed attempt, not proof of absence or divergence.
`unverifiable` means the accepted mechanism cannot check the subject and names
why. It does not mean verified.

In v1, Concord itself may execute only its already accepted Git probes. All
other verification is an attributed report from an authenticated trusted client
or a declaration that verification is unavailable. Adding another core probe
family requires accepted law for its network, credential, security, and result
semantics.

### D7. Freshness bounds are operator-approved per record kind

Each external-observation kind declares its acceptable verification age through
its ordinary reviewed definition/revision path. `freshness_policy_ref` and
`divergence_policy_ref` bind stable ID plus content hash, following CD-0036's law
revision identity; a same-ID policy edit cannot silently change existing
records. Callers cannot choose how long their own report remains actionable. A global value cannot represent both a Git
commit proof and volatile provider status.

At read time, age beyond the declared bound changes freshness to stale and
requires review. Elapsed time does not prove divergence, failure, absence, or
never-completable state. This follows #87: time changes attention posture only;
authority still comes from evidence.

### D8. Expected divergence must be declared before the check

A prior approved policy declares one of:

```text
none_expected
scoped_foreign_change
bounded_drift_window
```

The declaration names the exact subject fields/paths/scope and, where relevant,
the bounded window and linked resource claim. A mismatch is
`diverged_expected` only when that earlier declaration covers it. Otherwise it
is `diverged_unexpected`.

The verifier cannot excuse its own mismatch. Knowledge that foreign writers
exist does not prove that this particular change was expected. No similarity,
age, or frequency heuristic owns this classification.

### D9. Reads remain available; consequential use fails closed

Reads always return the record with provenance, verification state, freshness,
omissions, and attribution. TS3 read operations never append verification
events. An explicit D10 verification variant or a consequential preflight runs an
accepted Git probe and appends the result before the caller re-reads. Unverified, stale, partial, unreachable, and
unverifiable records remain inspectable but never render as verified or
`authoritative_empty`.

A negative/safety conclusion or consequential mutation that depends on the
record requires current verification covering the exact observation and subject
closure. The core may perform its accepted Git probe first. Otherwise the call
returns the existing `stale_requires_review` or `degraded_not_allowed` contract
without an effect.

A permanently unverifiable source may be used only through an explicit
operator-approved exception bound to the exact operation digest, subject, scope,
and versions. The exception authorizes that one action despite missing proof; it
never changes the record to verified or hides the omission.

### D10. The generic writer extends `observation_record`

The fallback uses the existing
`concord_work_define.observation_record` operation. Its current work-scoped,
grant-authenticated, idempotent, non-authoritative semantics remain. The input
gains a closed external-observation variant for capture or verification, and the
core emits purpose-built external-observation event payloads and folds. Plain CD-0030 observations remain plain statements and satisfy no evidence or
gate. The external variant is also non-authoritative: it may only supply or
withhold a precondition checked by another operation. It cannot positively
satisfy evidence, approval, transition, verdict, or completion.

Capturing a new observation remains forbidden on terminal work under CD-0030.
Appending a verification to an observation that was captured while work was
active remains allowed after terminal transition for audit and read-time
inspection; it cannot change terminal work or reopen authority.

Domain-specific records still write through their owning operations. A native
run uses workflow actions; Git knowledge uses Git proof; external conditions use
their condition path. The shared component does not create a second writer for
those domains.

### D11. CD-0039 is amended before implementation

Every CD-0039 native-run event embeds the shared capture component. Its
`reporting_authority_ref`, native subject, evidence, asserted time, and status
remain as accepted. The added component records capture method, observed
universe, freshness policy, divergence policy, and verification participation.

A native status may be folded and read as an attributed report while unverified.
Any later completion, safety conclusion, or consequential action that consumes
it follows D9. Reads never present `rolled_back` without its reporter and
verification/freshness state.

CD-0039 has no implementation or in-flight consumers, so this is a compatible
law amendment at the cheapest point in its lifetime. CD-0039 and CD-0040 land in
the same TS8 major implementation rather than adding verification in a second
migration.

### D12. Surface and replay obligations

No tenth tool is added. Purpose-built paths remain on their current operations;
the generic fallback extends an existing operation. TS7's authority, freshness,
omissions, watermarks, and stale/degraded refusals carry read status; no new
success/error vocabulary is needed.

Strict new input/output fields cannot be shown to old clients when omission
would make an unverified record look ordinary. The first implementation joins
the major surface amendment already required by CD-0037/CD-0039. Old clients
fail negotiation; there is no prose or untyped compatibility alias.

Existing plain work observations replay unchanged and never acquire external
provenance retroactively. Existing domain records retain their event history;
only new records written under the new major carry the component. Rebuild derives
verification projections solely from appended events.

## Rejected alternatives

**One universal external-state table.** It cannot enforce phase-specific native
status, Git proof, condition, backup, or domain invariants and would become an
informal string namespace.

**Domain records only.** It leaves recovery artifacts and genuine one-offs
unrecordable until a full domain model exists, recreating the silence behind
#89.

**Caller-authored completeness boolean.** It gives the producer authority to
claim it examined the whole world without a stable universe or completion
witness. SLSA removed comparable self-declared completeness booleans.

**`singleton|finite|open_world` as the scope model.** Cardinality is not
completeness. Twelve of fifteen is still a finite list.

**Set digest as completeness proof.** A digest proves what was hashed. It cannot
prove omitted subjects never existed unless the digested structure itself closes
the universe, as a Git tree does.

**Trust authenticated completeness claims without an anchor.** Authentication
attributes a report; it does not make the report complete.

**All divergence unexpected.** Safe but noisy for deliberately shared resources.
D8 keeps the default strict while allowing an earlier approved exception.

**Verifier-selected expectation.** It lets the reporting party excuse its own
mismatch.

**Advisory-only staleness.** It recreates #89: a well-formed stale record remains
actionable.

**Blocking reads.** It destroys historical evidence and prevents diagnosis.

**Generic workflow action or operator-only CLI.** A workflow action gives a
non-authoritative observation gate semantics; a CLI-only path removes agent
reachability and grant/idempotency provenance. CD-0030's existing operation is
the correct fallback boundary.

**Core HTTP/provider probes in v1.** They add network, credential, redirect,
SSRF, and provider-semantics authority without a concrete accepted need.

## Verification

- Deterministic tests cover verified match, stale verification, temporary
  unreachable, permanently unverifiable, expected divergence, and unexpected
  divergence.
- Coverage tests include complete singleton, complete pinned collection,
  truncated pagination, changing snapshot token, duplicate identities,
  filtered/partial authorization, open provider query, and bounded stream.
- Twelve observed items against an exact total of fifteen cannot validate as
  complete; an unknown total cannot validate as complete.
- Absence is impossible without D4's scope, anchor, completion, identity, and
  omission checks.
- Unverified records remain readable and visibly degraded; consequential use
  refuses before any effect.
- Operator exception is single-use/bounded and does not change verification
  state.
- Only accepted Git probes run inside Concord; non-Git reports remain attributed.
- Existing CD-0030 observations stay non-authoritative and cannot satisfy
  evidence, gates, or completion.
- CD-0039 native-run reads always carry attribution and
  verification/freshness state.
