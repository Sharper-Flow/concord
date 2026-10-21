# CD-0226: Review findings carry fixes with family evidence

- **Status:** Superseded by [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
- **Date:** 2026-10-15
- **Scope:** Suggested fixes in ranked findings, correction provenance, and independent review of applied fixes.
- **Amends:** [CD-0197](CD-0197-a-review-report-carries-a-typed-verdict-and-ranked-findings.md) D1-D5, [CD-0044](CD-0044-worker-evidence-caller-authentication.md) D1 and D4 for classification evidence only, and [CD-0058](CD-0058-no-model-routing.md) D2 for family evidence only.
- **Preserves:** [CD-0043](CD-0043-host-owned-lane-methodology.md), CD-0044 D2-D3 and D5-D6, [CD-0201](CD-0201-workflow-action-admission-is-derived-once-and-every-reachable-state-stays-live.md), [CD-0205](CD-0205-worker-jobs-bind-dispatch-and-separate-local-acceptance-from-delivery.md) accepted-delivery review anchoring, [CD-0206](CD-0206-an-accepted-no-ship-review-opens-the-settling-review-obligation.md), [CD-0220](CD-0220-repository-authoring-follows-lane-capability-and-approved-scope.md), and host-owned model selection under CD-0058 D1 and D3-D5.
- **Approval:** The operator accepted this record as written on 2026-10-15. The operator then directed host-owned model choice and delegated the family-policy amendment to the coordinator. Dual review approved the amended text. Implementation requires a separate assignment.

## Context

The typed review finding carries severity, confidence, detail, and an oracle tie, but no proposed repair.
A reviewer can identify a local repair without receiving permission to edit repository sources.
Keeping that repair with its finding avoids a separate reconstruction from prose.

[R6](https://github.com/Sharper-Flow/concord/blob/a98dfe5db3fa2af53bd2d82829aec840e94e64c4/.concord/docs/research/R6-typed-workers-and-model-routing.md), lines 59-62, records the historical absence of worker and model law.
CD-0017 supplied that law. CD-0058 then removed Concord model routing.
Neither record requires different model families for a reviewer-authored fix.
R6 section 5 also records that the synthetic same-model versus cross-model measurement did not exist.
This record claims no such measurement or general quality advantage.
Fix review needs a separate executing actor. Model-family choice belongs to each install, not Concord.

## Decision

### D1. Findings carry a bounded repair disposition

New fix-capable review contracts require `suggested_fix` on each delivery-blocking finding.
A delivery-blocking finding has severity `P0` or an oracle tie of `delivery_blocker`, `uncovered_case`, or `oracle_defect` (CD-0197 D4).
Other findings may carry it. Severity and oracle classification keep their existing separate meanings.
No mapping from host labels such as `HIGH` or `MEDIUM` replaces the closed `P0`-`P3` severity scale.

`suggested_fix` is a closed union:

- `patch`: an ordinary textual unified Git diff, its SHA-256 digest, and the exact repository Project and base commit it applies to.
- `needs_decision`: a bounded reason and the decision or scope boundary that prevents a safe patch.
- `unavailable`: a bounded reason why the reviewer cannot supply a complete local repair.

A patch contains 1 to 16384 UTF-8 bytes. A reason contains 1 to 512 UTF-8 bytes.
All suggested fixes in one report together occupy at most 32768 serialized UTF-8 bytes.
Existing whole-report and shared-context limits still apply. Refuse overflow whole, never truncate a patch or silently discard a finding.
Binary patches, absolute paths, parent traversal, and external targets are not valid patch content.
The payload contains only diff bytes, never shell commands to run around them. A textual diff may legitimately change a script or its executable mode.
Patch paths must remain within the correcting job's authorized path scope, including any separately authorized caller repair. Review itself gains no edit scope.
A missing permission or unsettled design returns `needs_decision`, not a patch that assumes approval.
An unavailable patch does not weaken or close its finding.

The canonical finding ID, severity, confidence, oracle tie, and verdict couplings remain unchanged.
Suggested fixes are reported content, not executable actions, workflow verdicts, or proof that a finding is resolved.

### D2. An authorized author applies and tests the fix

The review lane stays non-editing under CD-0220 D3.
The coordinator carries the exact fix and its source finding/attempt references through the existing typed correction context.
The packet binds its digest and base. It does not flatten patch bytes into truncated finding detail or free-text strategy.
Strategy explains the intended correction beside that typed content.

The editing actor checks applicability, applies the patch through its permitted tools, and runs the narrow checks before result acceptance.
No boundary automatically applies reviewer text or executes commands carried in it.
A base mismatch, failed application, failed check, or disputed design returns to ordinary correction or the existing decision owner.
It does not select an unchecked fallback or create an approval.

The correction record binds the originating canonical finding, fix-producing attempt, patch digest, applying attempt, and actual repaired subject.
An authorized coordinator may mechanically apply an unchanged suggestion, recording its real executing actor rather than an invented worker attempt.
A model-authored adaptation must run through an editing attempt with authenticated model readback.
The report records whether the suggestion was applied unchanged, adapted, or replaced, with evidence references for the actual delta.
Adaptation does not erase the suggestion's authorship or make its originating family independent.
Changes outside the approved repair return to the scope owner.

### D3. The applied fix requires a separate reviewer, not a different family

On new fix-capable workflow versions, applying or adapting a reviewer-supplied patch opens a fix-review obligation.
Before finding closure or whole-work delivery, a separate review must cover the actual repaired subject and the source finding.
Local acceptance may record the repair while this review debt remains open. It proves neither finding closure nor delivery.
The subsequent review uses CD-0205's existing accepted implementation anchor and current clean qualified subject.
The review's executing actor must differ from the actors that proposed and applied the fix under CD-0013 distinctness.
A wholly independent replacement records that claim for independent review.
Renaming or reformatting a patch cannot erase its authorship or satisfy this obligation.

An install that wants cross-family review chooses its reviewer models in host configuration under [CD-0054](CD-0054-routing-policy-host-authority.md) and CD-0058.
Concord imposes no model-family requirement on fix review. It accepts another review's cost without claiming measured superiority.

Family identity means the underlying model lineage, not its provider endpoint, spelling, version, or host alias.
Different versions or endpoint aliases of the same lineage do not establish independence.
The trusted host supplies a canonical family classification of each actual `readback_model`, with the classification source's digest.
It authenticates that classification through the existing worker-evidence assertion, bound to the exact attempt and readback model.
The signed enumeration gains the canonical family and classification-source digest, with matching shared encoding vectors.
The core compares these recorded family identities. It never guesses them from model-name substrings or accepts worker-authored family text.
The review record reports one comparison of the reviewer's family with the families that proposed or adapted the fix.
A missing, ambiguous, or conflicting classification is unknown.
A known match reports the same family. Report different families only when every contributor classification is known and differs from the reviewer's known family.
Otherwise the comparison reports unknown and claims no family independence.
The host owns classification accuracy. This evidence does not independently prove a vendor's undisclosed model ancestry.
The classification source is host configuration outside the repository. No Concord code path or repository file maps a model identifier to a family.

The existing authenticated worker-evidence boundary gains a native, host-only `worker-family-evidence` verb.
It appends classification evidence under the work aggregate, bound to the exact retained attempt and its immutable readback identity.
The signed assertion binds the verb, client, work, attempt, readback, family, source digest, any superseded classification reference, issue time, and nonce.
The existing `worker_evidence` capability, signature verification, nonce transaction, and revocation checks apply. No agent-tool grant exposes this verb.
A correction must name the earlier classification it supersedes and retain a reason and evidence references. Original evidence remains readable.
This narrow amendment to CD-0044 D4 allows classification side records after terminal completion, never another completion, failure, or changed readback model.
Shared admission derives the current classification from these records and refuses ambiguous or incomplete corrections.
A review records the exact classification references its observation used, or their absence. Replay evaluates each event against its recorded prefix, never current host configuration.
A later classification correction updates the reported observation without reopening fix-review debt or changing recorded acceptance, before or after delivery.
The correction retains the earlier observation and evidence. It does not undo an external effect or rewrite history.
A host can therefore supply missing evidence or correct inaccurate classification without rewriting terminal outcome history or dispatching a fictitious author.

This rule amends CD-0058 D2 only by adding authenticated classification of the sole observed model identity.
It creates no intended-model field, repository model pins, provider inventory, fallback selection, or Concord model resolver.
Host configuration still chooses what executes.
Neither model family nor classification availability gates dispatch, result acceptance, finding closure, or delivery.
Existing model-readback, failed-attempt, approval, and escalation boundaries remain in force. Missing family evidence does not automatically launch another attempt.
A contract revision alone does not erase debt for bytes still delivered. Any changed obligation requires the existing approval boundary and an evidenced disposition of those bytes.
Neither an approval token nor a caller assertion alone discharges that debt.
Implementation must prove a reachable recovery for every new refusal before it enables the new definitions.

### D4. Retain the existing contract, debt, and history owners

`contracts/agent-lane-report.schema.json` owns the closed fix shape beside `review_finding`.
`internal/store/worker_lanes.go` validates and retains the fix without rescoring the finding.
`adapter/opencode/dispatch.ts` admits the same whole values, while the host evidence boundary records readback and family provenance.
The packet schema, `adapter/opencode/packet.ts`, and `internal/store/work_context.go` carry the exact typed correction and source references.
The shared workflow admission and existing review-debt owners enforce fix-review debt at finding closure and both delivery routes, while admitting local repair acceptance with debt open.
`internal/agent/worker_evidence.go` owns the expanded signed shape and host-only classification-evidence authentication, with the existing CLI and adapter boundary owners.
`internal/store/workflow_outcome.go` enforces actor distinctness and reports family comparisons from recorded evidence, not configured intent.
`scripts/generate-agent-lanes.py` projects report obligations without adding an editing capability to review.

The implementation must introduce explicit payload upcasts, new lane digests, and new workflow-definition versions.
Released definitions remain immutable under CD-0115.
Historical absence remains no fix or family evidence. Replay fabricates no patch, family, or satisfied review.
New fix-review obligations do not retroactively invalidate accepted historical results.
Existing CD-0206 settling-review debt, oracle blocker closure, independent verification, and operator gates remain cumulative requirements.

## Alternatives considered

- Let the reviewer apply its own fix. Rejected: this violates its non-editing capability and obscures authorship.
- Put the patch in finding detail. Rejected: bounded detail can truncate the repair and lacks its own subject and digest.
- Require every finding to contain a patch. Rejected: some findings require a scope or design decision instead of code.
- Compare provider names or unequal model strings. Rejected: aliases and versions can name the same family.
- Pin models in the repository. Rejected: CD-0058 keeps selection and model access with the host.
- Mandate cross-family fix review. Rejected: model choice belongs to each install, and a single-family install must remain usable.
- Put the family policy in a workflow definition. Rejected: an install cannot change Product workflows without a fork.
- Drop family evidence. Rejected: an install that wants cross-family review loses its evidence basis.
- Add a host-only signed policy field. Rejected: assertions bind individual attempts, so conflicting declarations would need a new governing-policy rule for an optional check.
- Treat tests alone as fix review. Rejected: passing checks do not establish semantic correctness or independent judgment.

## Consequences

The finding retains a concrete repair or a reason why one cannot safely be supplied.
The author retains responsibility for applicability, tests, scope, and the actual submitted bytes.
An install can close and deliver a qualified fix review with the same or unknown model families. Actor distinctness remains mandatory.
Host classification becomes another authenticated evidence input, with an explicit accuracy limit and no model-selection authority.
An install's host configuration bears the cost of any cross-family requirement it chooses.
Coordinator integration may apply unchanged bytes, but must delegate model-authored adaptation to an evidence-backed editing attempt.
This record creates no runtime behavior until a separately authorized implementation passes verification.

## Verification

- `python3 scripts/check-doc-contract.py` checks the registered outline and prose. Draft review must call its `check_record` function with proposed-record metadata before registration.
- `python3 scripts/check-knowledge-closure.py` reports an unregistered draft as unprocessed. Acceptance must register the approved revision and pass strict closure without asserting implementation coverage.
- `go test ./internal/store` must cover the fix union, byte limits, aggregate overflow, malformed patches, retained finding IDs, unavailable repairs, and historical upcasts without fabricated fixes.
- `bun test adapter/opencode/dispatch.test.ts adapter/opencode/packet.test.ts` must prove whole-value admission, exact correction transport, preserved patch bytes, and unchanged non-editing review permissions.
- `go test ./internal/store` must prove that local repair acceptance admits with debt open and creates the accepted review anchor, while finding closure and both delivery routes refuse until qualified fix review.
- `go test ./internal/agent ./internal/store` must cover signed family side records, missing and corrected terminal classifications, immutable terminal outcomes, nonce replay, and absence of an agent grant route.
- `go test ./internal/store` must prove prefix-bound replay, retained observations after classification corrections, and unchanged debt and acceptance before and after delivery.
- `go test ./internal/store` must preserve settling review, oracle closure, actor distinctness, and correction limits, and refuse adaptations without authenticated model readback.
- `go test ./internal/store` must prove debt for applied and adapted fixes, exact subject/finding joins, and reported same, different, and unknown families, including aliases and conflicting classifications.
- `go test ./internal/store` must prove that family comparisons and absent classification evidence never gate dispatch, result acceptance, finding closure, or delivery.
- `python3 scripts/generate-agent-lanes.py --check` and `python3 scripts/check-agent-contracts.py` must pass after regeneration. Independent review must check actual applicability and repair quality rather than treat schema acceptance as proof.
