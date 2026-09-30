# CD-0035: Governing requirements are scope-bound and refused at capture

- **Status:** Accepted
- **Date:** 2026-08-17
- **Scope:** Agent capture path; typed-error options affordance; issue #167
- **Related:** CD-0006 (D5, D10), CD-0012 (D6), CD-0015 (R0), TS1 AJ3, TS4
  ([`agent-mutation-tool-contract.md`](../agent-mutation-tool-contract.md)),
  [`specs-as-laws.md`](../specs-as-laws.md) §4
- **Supersedes:** nothing

## Context

TS4 already states the behaviour: "A governing-law conflict returns a typed
conflict and no mutation; the agent cannot silently shrink the accepted scope."
CD-0006 D5 already states who resolves it: "Human approval is always required to
enact law changes or governed scope cuts." `specs-as-laws.md` §4 already names
the three options the operator chooses between.

None of that was reachable from a mutation. The capture path validated project
membership, workflow-type refs, and cross-Product approval, and nothing else; no
data model named a governing requirement; and the wire envelope had no field
capable of carrying an operator choice. TS1 `AJ3-spec-conflict` — one of the
twenty-one accepted scenarios — asserts exactly this refusal and had to be
deferred because the behaviour did not exist.

The accepted law was therefore prose-only at the one boundary where it binds.
That is the gap this record closes.

## Decision

**D1 — `options` is a typed-error affordance, not a general envelope field.**
`TypedError` gains `options`: a bounded, unique list drawn from the closed
vocabulary `clarify`, `amend_contract`, `accept_scope_cut`. The field is
*permitted, never required*, so every existing `invariant_violation` emitter —
notably the unsatisfied spec mandate at completion — is unchanged. A non-empty
`options` couples structurally: the error kind must be `invariant_violation` and
the recovery action must be `contact_operator`. Both Go validation and the
canonical JSON schema carry the coupling, mirroring the existing
`ambiguous_scope` ↔ `resolve_ambiguity` ↔ non-empty `candidates` rule.

No new recovery-action kind is minted. `contact_operator` already means *return
to the operator for direction*, which is precisely the consequence of a
governing-law conflict.

**D2 — Governing requirements bind to scope, not to rules.** A governing
requirement is declared against a Project and applies to work captured into it.
It is not a field on a law, a rule, or a spec clause. CD-0015 R0 forbids "a
per-rule obligation field or heuristic persistence", and this record does not
introduce one: the requirement is an explicit scope-level declaration with its
own event and projection.

**D3 — Refusal is computed by set difference, never by reading prose.** At
capture the core resolves the requirements applicable to the target scope,
compares them against the requirement set the capture declares, and refuses when
the difference is non-empty. The refusal carries `invariant_violation`, the three
options, `contact_operator` recovery, and emits no events at all.

Concord never judges whether an instruction *intends* to omit an obligation.
Semantic omission is not detectable structurally and any attempt would be a
heuristic owning correctness. What is detectable — and what the accepted law
actually forbids — is capturing work into a governed scope while declaring a
requirement set that does not cover the scope's requirements.

**D4 — Enumeration confers no authority.** The declared set is not a
self-assertion of compliance. A caller can only match the applicable set or fail;
it cannot assert satisfaction, weaken a requirement, or mark one inapplicable.
The single path to a reduced set is an operator-approved scope cut carried by the
existing core-issued approval machinery — the legislative moment CD-0006 D5
requires, recorded auditably rather than taken silently. This preserves the
call-authority rule that caller prose and self-asserted booleans confer no
authority.

**D5 — One vocabulary, recorded.** The wire vocabulary is the TS1 corpus wording
(`clarify`, `amend_contract`, `accept_scope_cut`), which is accepted contract.
The prose variants say the same three things and are bound here so they cannot
drift apart:

| Wire (TS1, binding) | `specs-as-laws.md` §4 | CD-0012 D6 |
|---|---|---|
| `clarify` | Clarify intent | Clarify |
| `amend_contract` | Evolve the spec | Revise the end-state |
| `accept_scope_cut` | Consciously accept scope reduction | Consciously defer |

## Rejected alternatives

**A general `Envelope.options` field.** Wider reach for a need that does not
exist yet: no accepted scenario requires an operator choice on an `ok`,
`pending`, or `partial` outcome. It would add a field to `$defs.base` and all
four outcome variants, each closed by `unevaluatedProperties: false`, to serve
one error path. Rejected as speculative contract surface; D1 can widen later
without breaking a narrower start, while the reverse is not true.

**`error.details["options"]`.** Zero contract widening and zero digest churn, and
for that reason tempting. Rejected because it puts a closed three-value operator
choice into an untyped `map[string]any`. That is the exact failure mode repaired
in #168, where `version_conflict` and cycle refusals declared invariants in types
that were never populated and left agents parsing English prose to recover.
Reintroducing it for the operator-choice surface would be a regression by
precedent.

**Extending the workflow-contract `spec_mandate` to capture.** Cheaper — the
table, fold, and completion check already exist. Rejected because `spec_mandate`
is planning-approved and completion-checked by construction (CD-0006 D10), and
because capture usually carries no `workflow_type_ref` at all. Most captures
would bypass the gate entirely, which would make the floor claim read stronger
than the mechanism actually is.

**Detecting omission from the instruction text.** Rejected as a heuristic owning
correctness. It cannot be falsified by a deterministic oracle, so it could not
carry a TS1 scenario, and a wrong verdict in either direction is worse than no
verdict: a false positive blocks honest capture, a false negative is the silent
scope cut the law exists to prevent.

**A per-rule obligation field.** Forbidden by CD-0015 R0 and not revisited here.
