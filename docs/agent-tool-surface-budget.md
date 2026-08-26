# Concord Agent Tool-Surface Budget and Granularity (TS2)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS2; binding input to CD-0005 §2 and TS3–TS9.
> **Binding input:** accepted TS1 canonical jobs and 23-scenario corpus.
> **Does not decide:** exact tool names/count within the budget, read operations
> (TS3), mutation operations (TS4), call context (TS5), transport (TS6), result
> envelopes (TS7), current manifest identity/change evidence (TS8), or measurement evidence (TS9).

## Context

TS1's eight evidenced job families need a governed tool surface that avoids
both CRUD explosion and mega-dispatch. The binding inputs are the accepted
TS1 canonical jobs and their 23-scenario corpus, PM1's bounded-query
requirements, the Advance postmortem's large-catalog lessons, and the
external tool-design evidence TS1 records. This record fixes the budget and
the granularity rule that TS3 and TS4 chose inside, and the evaluation law
any candidate surface must pass.

## Contract

The binding contract is sections 1 through 3 and 5: the budget decision and
the static-surface rule, the granularity rule with its sharing conditions
and mandatory split boundaries, the budget accounting that counts every
always-visible schema, and the selection rule for comparing candidates.
Section 4 records the candidate comparison; sections 6 through 8 record
evidence, rejected shortcuts, and falsifiers, and carry no obligation.

## 1. Decision

Concord v1 exposes **at most ten always-visible domain tools** to an agent. TS3
and TS4 chose eight tools inside that cap; CD-0041 added two more under the
accepted amendment recorded at the end of this record. Budget:
`always_visible_tools: 10`. The canonical machine-readable count is the
generated manifest's `surface.tool_count`
([`contracts/agent-tool-surface.v1.json`](../contracts/agent-tool-surface.v1.json)),
and `scripts/test-agent-contracts.py` fails if this document and the manifest
disagree.

The cap is a Concord governance constraint derived from TS1's eight evidenced job
families and the explicit need to avoid both CRUD explosion and mega-dispatch. It
is not asserted as a universal model limit across models, schemas, or tasks.

The current surface is **static and deterministic**. No catalog, tool-search, or
dynamic-discovery meta-tool is added. A future discovery proposal would require a
named deterministic failure and TS8/TS9 evidence for its own selection, context,
latency, and schema cost.

## 2. Granularity rule

A tool groups a **closed family of domain intents**, not entities, tables, API
endpoints, workflow phases, or CLI commands.

Operations may share one tool only when all of these are true:

1. callers choose them for the same recognizable intent family;
2. they resolve scope and authorization through the same authority boundary;
3. they share the same consequence and approval class;
4. they have compatible bounded input and result families;
5. combining them does not create a mostly-empty parameter bag; and
6. typed operation variants can state each operation's required fields and errors
   without prose-only conditional rules.

A boundary **must split** when any of these differ materially:

- human-approval or external-consequence boundary;
- authoritative system or transaction boundary;
- synchronous domain operation vs durable asynchronous operation;
- retry/idempotency semantics;
- success oracle or recovery path;
- output scale or pagination behavior;
- likely caller intent, such that similar descriptions cause wrong selection.

Sharing an entity type is not enough to merge operations. Touching several entity
types is not enough to split an end-to-end intent. A closed typed `operation` union
is allowed; arbitrary command names, free-form dispatch, generic JSON patch, and
unbounded parameter maps are not.

## 3. Budget accounting

### Always-visible budget

The tool cap counts every agent-callable schema included without an explicit
discovery step, including any status, catalog, or helper tool. Renaming a helper as
"meta" does not remove its prompt and selection cost.

Resources, prompts, ordinary context records, and operator-only CLI commands are
not tools and do not count. They still require their own authorization and output
bounds when later decisions expose them.

### Schema/context budget

TS2 sets no absolute token number before TS3–TS7 define concrete schemas and the
chosen clients/tokenizers can measure them. Every candidate must record:

- total always-visible schema tokens and bytes;
- per-tool description and input-schema size;
- fields unused in each TS1 scenario;
- output bytes and follow-up calls per completed job; and
- selection ambiguity between neighboring tools.

The accepted candidate is the smallest passing surface, not the candidate with the
fewest declarations in isolation. Compressing several distinct consequence or
recovery boundaries into one tool to save prompt tokens is a failure.

## 4. Candidate comparison

| Candidate | Shape | TS1 fit | Decision |
|---|---|---|---|
| **Entity/CRUD surface** | Separate list/get/create/update/delete tools for Product, Project, work, relation, evidence, note, and operation entities; likely 20+ tools. | Agents must reconstruct AJ1–AJ8 by sequencing storage operations; high overlap; leaks implementation shape. | **Rejected.** |
| **Two mega-tools** | One generic query dispatcher and one generic mutation dispatcher. | Minimizes count but combines unrelated intents, approvals, authorities, recovery, and result shapes; conditional schemas become prose. | **Rejected.** |
| **One tool per TS1 job** | Eight tools mirroring AJ1–AJ8 exactly. | Useful comparison baseline, but assumes an end-to-end evaluation job always equals one callable boundary. AJ1 combines several read intents; AJ8 spans native operations and durable status. | **Not binding.** Evaluate, but do not force 1:1 mapping. |
| **Domain-intent surface** | A single-digit set of cohesive read, mutation, knowledge, and operation families; closed typed variants where the granularity rule permits. | Preserves TS1 outcomes while allowing TS3/TS4 to merge always-chained reads and split distinct consequence boundaries. | **Selected rule.** Exact map remains TS3/TS4. |
| **Progressive discovery first** | Small meta-surface searches or loads additional domain tools. | Solves a scale problem Concord does not yet have; adds a meta-selection step before evidence. | **Rejected for the current path.** Reconsider only through TS8/TS9. |

## 5. Candidate evaluation

TS3/TS4 candidates run the accepted TS1 corpus plus PM1 read scenarios. The runner
records, per scenario and candidate:

- hard-oracle pass/fail;
- correct initial tool selection and wrong-domain calls;
- total calls and retries;
- schema/prompt tokens and output bytes;
- typed-error recovery and operator interventions;
- latency, including discovery when present; and
- parameter sparsity by tool/operation variant.

### Selection rule

1. Reject any candidate that fails a hard scenario oracle, crosses an authority or
   consequence boundary, requires callers to sequence domain invariants manually,
   or exceeds ten always-visible tools.
2. For a deterministic corpus run, compare success and recovery counts directly.
   For stochastic evaluation, predeclare the model, run count, sampling settings,
   and confidence interval before comparing candidates; reject a candidate whose
   interval establishes a success or recovery regression.
3. Choose the remaining candidate with the fewest always-visible tools, then the
   smallest total schema/context cost, then the fewest calls and retries.
4. If no candidate within the recorded budget passes, do not add one more by
   default. Identify
   the named failing TS1 scenario and revisit the granularity rule, TS1 job model,
   or dynamic-discovery evidence explicitly.

Tool count alone never outweighs scenario correctness or a real authority,
approval, transaction, or recovery boundary.

## 6. Evidence basis

- TS1 establishes eight end-to-end jobs and records tools exposed/considered,
  calls, retries, schema tokens, output bytes, latency, interventions, and outcome.
- PM1 rejects repeated list→show→search choreography and requires bounded,
  typed queries rather than free-form SQL or dispatch.
- Advance's large tool catalog and repeated target-path/repair surfaces are
  predecessor evidence against mechanical command-to-tool mapping, not a candidate
  Concord surface (`feature-inventory.md`, `advance-postmortem.md`).
- Anthropic recommends consolidating related operations and keeping tool sets
  focused to reduce selection ambiguity:
  <https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools>.
- OpenAI recommends strict schemas and explicit descriptions of purpose,
  parameters, and results:
  <https://developers.openai.com/api/docs/guides/function-calling>.
- Published large-catalog results support adaptive retrieval when the candidate
  universe is genuinely large, but do not establish a universal small-surface
  ceiling: <https://arxiv.org/html/2605.24660v2>.

## 7. Rejected shortcuts

- One tool per table, entity, endpoint, CLI command, or workflow phase.
- One untyped `invoke`, `query`, `update`, or JSON-patch escape hatch.
- Optional parameters whose legal combinations exist only in prose.
- A catalog/discovery tool added merely because another platform has one.
- Permanent aliases counted outside the budget.
- Hiding a durable asynchronous operation behind a synchronous success response.
- Merging approval-required and non-consequential operations solely to lower count.

## 8. Falsifiers and amendment rule

Reopen TS2 when:

- no structurally valid TS3/TS4 candidate within the recorded budget passes all
  TS1 scenarios;
- a repeated scenario shows two tools are indistinguishable to supported agents;
- two tools are always chained and a merged candidate improves outcomes without
  crossing a required split boundary;
- measured schema/context cost causes failures despite correct job boundaries;
- a growing accepted surface makes static exposure materially worse than measured
  progressive discovery; or
- a supported client cannot expose the accepted static surface without changing
  its semantics.

Any expansion, split, merge, discovery layer, or alias after launch follows TS8/TS9;
it does not silently amend this budget.

## Approved amendments

**2026-08-26 — the budget is amended from nine to ten under CD-0041.**
CD-0041 added two always-visible tools through issues #196 and #197:
`concord_domain` (Product → Domain navigation and Domain detail reads) and
`concord_work_initiative` (the Initiative grouping surface). Each addition
carried the full TS8 change evidence — named scenario, canonical manifest
and generated artifacts updated together, strict schema and conformance
proofs, and recorded operator acceptance (issue #195 for the overlap
operation) — but the budget consequence was never recorded here, and TS2's
own rule says expansion never silently amends this budget. The surface has
therefore run ahead of this record since #197.

The operator approved recording the budget at ten rather than folding the
surface back under nine. The budget's authority is now structural in both
directions: the generator refuses any manifest whose `surface.tool_count`
disagrees with its actual tool list, and
`scripts/test-agent-contracts.py` fails when this document's
`always_visible_tools` value disagrees with the manifest. A future
expansion must amend this record, the manifest, and the generated artifacts
in one change, with the TS8 evidence, or CI fails.

## Acceptance criteria

- Given the generated manifest
  When its surface is counted
  Then the always-visible tool count equals the budget this record
  declares, and every tool in the manifest is counted.

- Given a tool section in the manifest
  When it is validated
  Then it is closed — identifier, description, and declared operations
  only — and every declared operation exists exactly once.

- Given any tool or operation entry
  When aliases are searched for
  Then none exist; permanent aliases are not permitted outside the budget.

- Given a candidate surface for evaluation
  When it is judged
  Then a candidate that crosses an authority, approval, transaction, or
  recovery boundary is rejected regardless of its tool count.

## Verification

The budget and shape laws are enforced by the generator and the contract
tests, so every criterion carries a typed exemption naming the enforcing
mechanism.

- Criterion 1 is proved by the manifest equality pins of
  `scripts/generate-agent-contracts.py` (exact tool count, unique
  identifiers, `surface.tool_count` agreement) and the document-manifest
  join test of `scripts/test-agent-contracts.py`
  (`Ts2BudgetTests.test_document_budget_matches_manifest`).
- Criterion 2 is proved by the generator's closed-tool-section and
  operation-coverage checks, exercised by `ManifestTamperTests`
  (`scripts/test-agent-contracts.py`).
- Criterion 3 is proved by the generator's alias rejection.
- Criterion 4 is law for future evaluations; it is enforced by the TS8
  change rule, which requires a named scenario and generated-artifact unity
  for any surface change, and by TS1's corpus, which a boundary-crossing
  candidate fails. Section 8 records the falsifiers.
