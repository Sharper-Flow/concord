# CD-0024: Epic Agent Surface and Narrow TS9 Exception

**Status:** Accepted under operator approval.
**Approval date:** 2026-08-14.
**Approval:** Operator approved surface `3.0.0` for issue #128 and the narrow TS9
exception after the supported-model evidence gate was surfaced as unavailable.
**Type:** CD-0009 reachability implementation and TS8/TS9 amendment.
**Issue:** [#128](https://github.com/Sharper-Flow/concord/issues/128).
**Amends:** CD-0009 D1/D1a; TS8 and TS9.
**Preserves:** CD-0005's generated canonical manifest, TS5 authority envelopes,
CD-0010, and CD-0009's Epic constraints.

## Context

CD-0009 already accepts Epic as a Product-scoped canonical work-item kind with a
living narrative and ordered entries. The store has event folds, validators, event
builders, bounded reads, and conformance tests. No agent operation could call them.
The operational coverage record therefore marked initiative framing and promotion
provenance not covered: hand-appending events is not a reachable outcome.

Making the model reachable adds the ninth static agent tool and seven operations.
TS8 classifies that as a major change. The normal TS9 gate requires paired
supported-model trials, but this repository has no agent-tool TS9 runner or bounded
evidence-artifact pipeline; its Promptfoo harness evaluates worker lanes, not
`concord.ts` selection against PM1/TS1 jobs. Unit tests cannot be represented as
model trials.

## Decision

**D1. Surface `3.0.0` adds `concord_work_epic`.** The generated manifest owns one
create operation, ordered-entry add/remove/reorder/requiredness operations, narrative
revision, and bounded entries read. The operations call CD-0009's existing event
builders; they do not invent a second Epic lifecycle, direct parent-edge writer,
nested Epic, or cross-Product path.

**D2. Epic creation starts with an empty narrative.** CD-0009 D1a makes
`epic.narrative_revised` the only narrative write path. Create then revise is the
intentional route; no create-time narrative bypass is added.

**D3. TS9 has one narrow exception.** For this `2.3.0 → 3.0.0` change only, the
paired supported-model trial requirement is waived. The substitute evidence is:

- the named, accepted CD-0009 Epic outcome and #128's unreachable-surface proof;
- positive and rejection-path agent-boundary tests for every operation;
- generated manifest/schema/fixture/Go/TypeScript drift checks;
- explicit Product-scope preflight before Epic creation; and
- fail-closed v2 bootstrap, durable-operation replay compatibility, migration
  guidance, and this explicit operator approval.

This exception does **not** claim tool-selection quality, model success, or a TS9
baseline. It does not authorize an additional tool, operation, alias, discovery
surface, or future TS9 exception. Before any later model-visible surface change, the
normal TS9 runner and evidence artifact must exist and evaluate the 3.0.0 baseline.

**D4. The major boundary fails closed.** A 2.x adapter has a different pinned digest
and cannot express the ninth tool. Core serves new grants only at 3.0.0; a 2.x range
fails before a domain call. Upgrade the generated adapter/manifest and request a
fresh 3.0.0 grant. Durable events accepted under 1.x/2.x remain readable and
recoverable; no history is rewritten.

## Rejected alternatives

**Claim unit tests as model evidence.** Rejected: deterministic conformance proves
contract behavior, not model tool selection or task success.

**Ship as a minor release.** Rejected: adding a tool/operation is a TS8 major change
and strict clients cannot receive an unknown static tool set losslessly.

**Keep Epic unreachable until a full runner exists.** Rejected by operator direction:
the accepted Epic model is included in the first usable floor, and the bounded
exception retains every structural/authority guard while making its existing state
reachable.

## Verification

- Contract version/digest negotiation accepts 3.0.0 and refuses 2.x before grant
  issuance.
- Every Epic operation has generated schema fixtures and agent-boundary positive or
  rejection coverage.
- An ambiguous/multi-Product Epic create leaves no work item behind.
- Entry mutation uses the Epic event builder, preserving the typed parent relation
  and ordered `epic_entries` provenance atomically.
- `bin/oc-test full` and contract drift checks pass.
