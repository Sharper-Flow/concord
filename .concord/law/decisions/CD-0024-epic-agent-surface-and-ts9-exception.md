# CD-0024: Epic Agent Surface and Narrow TS9 Exception

**Status:** Accepted under operator approval.
**Approval date:** 2026-08-14.
**Approval:** Operator approved surface `3.0.0` for issue #128 and the narrow TS9
exception after the supported-model evidence gate was surfaced as unavailable.
**Type:** Epic reachability implementation and TS8/TS9 amendment.
**Issue:** [#128](https://github.com/Sharper-Flow/concord/issues/128).
**Amended by:** [CD-0222 D9](CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md#d9-migration-reaches-the-clean-vocabulary-compatibility-is-bounded)
removes the Epic agent tool that D1 added.
**Current surface authority:** [CD-0042](CD-0042-pre-go-live-single-path.md),
[TS8](../specs/agent-tool-surface-evolution.md), and
[TS9](../specs/agent-tool-surface-measurement.md) govern pre-go-live identity and evidence.
This record is historical evidence, not a current compatibility path or reusable
TS9 exception.
**Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2 and D13 — the store keeps current rows; the cutover is one documented migration.
**Amends:** TS8 and TS9.
**Preserves:** CD-0005's generated canonical manifest, TS5 authority envelopes,
CD-0010, and the accepted Epic constraints.

## Context

The accepted research-context law already accepted Epic as a Product-scoped canonical work-item kind with a
living narrative and ordered entries. The store validates and reads Epic state
with bounded reads and conformance tests. No agent operation could call them.
The operational coverage record therefore marked initiative framing and promotion
provenance not covered: hand-writing store state is not a reachable outcome.

Making the model reachable adds the ninth static agent tool and seven operations.
TS8 classifies that as a major change. The normal TS9 gate requires paired
supported-model trials, but this repository has no agent-tool TS9 runner or bounded
evidence-artifact pipeline, and no harness evaluates `concord.ts` selection
against PM1/TS1 jobs. Unit tests cannot be represented as
model trials.

## Decision

**D1. Surface `3.0.0` adds `concord_work_epic`.** The generated manifest owns one
create operation, ordered-entry add/remove/reorder/requiredness operations, narrative
revision, and bounded entries read. The operations call the store's Epic
writers; they do not invent a second Epic lifecycle, direct parent-edge writer,
nested Epic, or cross-Product path.

**D2. Epic creation starts with an empty narrative.** The accepted narrative rule makes
revision the only narrative write path after create. Create then revise is the
intentional route; no create-time narrative bypass is added.

**D3. TS9 has one narrow exception.** For this `2.3.0 → 3.0.0` change only, the
paired supported-model trial requirement is waived. The substitute evidence is:

- the named, accepted Epic outcome and #128's unreachable-surface proof;
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
fresh 3.0.0 grant. Retained state migrates through the one documented cutover with a verified snapshot ([CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)).

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
- Entry mutation writes the typed parent relation and ordered `epic_entries`
  provenance in one transaction.
- `bin/oc-test full` and contract drift checks pass.
