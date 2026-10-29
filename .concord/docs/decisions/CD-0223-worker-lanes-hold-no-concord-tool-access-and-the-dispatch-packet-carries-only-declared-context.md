# CD-0223: Worker lanes use declared context and bounded host capabilities

- **Status:** Accepted
- **Date:** 2026-10-10
- **Scope:** The tool access of dispatched worker lanes, the adapter surface
  those lanes reach, and the packet block that carries law and Domains to a
  lane; [Concord (CON) issue 899](https://linear.app/sharper-flow/issue/CON-899)
- **Preserves:** CD-0017 D1 to D9 at every other sentence, CD-0005 D6, and the
  coordinator routes every lane's parent already uses
- **Related:** CD-0017, CD-0020, CD-0043, CD-0102, CD-0193, and [CD-0222](CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md)
- **Terms:** End-of-file (EOF) marks the end of a source. Git's current commit reference (HEAD) identifies the checkout commit.
- **Approval:** Operator-delegated approval A4 for [Concord (CON) issue
  899](https://linear.app/sharper-flow/issue/CON-899).

## Context

CD-0017 D4 binds worker authority: a worker run is a bounded execution
attempt, and durable workflow authority stays with the owning workflow. The
decision bounds what a worker records. It does not bound what a worker reads
or writes through the Concord tool surface.

Measured dispatch history shows the gap is real. Implement-lane sessions made
1424 Concord calls, and most were refused as invalid input. The review,
research, design, and verify lanes show the same pattern. Lanes also wrote
Concord state: captures, relations, and lesson publishes rode lane sessions.
No lane capability grants that access, and no lane definition mentions it.

The reads have one recurring cause. A lane fetched Domain law, research
findings, and prior-attempt state because its packet carried no such block
and nothing told the lane the packet was complete. Each fetch spent a turn on
a refused or mis-scoped call.

## Decision

A dispatched worker lane holds no general Concord state-query or mutation
access. The dispatch packet is its complete Concord context. The host may
expose bounded capability tools for declared pinned readings, scoped review
diffs, and recorded executable checks. Those tools add no context inventory
or workflow authority.

1. Generated lane definitions deny every Concord tool unless the closed
   capability mapping explicitly binds it to a capability the lane holds.
   Utility definitions retain their complete Concord-tool denial. Tool names
   and schemas come from the existing agent and host tool-surface contracts.
   A new tool without a capability binding remains denied by default.

2. Every general Concord state or workflow entry retains its managed-parent
   check and structured no-effect refusal. A root coordinator passes unchanged.
   A bounded worker capability instead requires the exact active attempt and
   authorized packet, authenticated host child, lane capability, and dispatched
   worktree. It cannot call a general Concord operation on the worker's behalf.

3. Every lane definition states that its packet is complete. A pinned-reading
   capability returns only bytes of an original declared reading. A diff
   capability returns only the pinned review delta within the approved job's
   path scope. A check capability selects only recorded executable controls.
   A lane reports missing context or executable backing and fails when the gap
   blocks its assigned result. None of these capabilities authorizes a worker
   transition, verdict, acceptance, approval, or completion.

The packet carries the one fact lanes still fetched. The workflow law context
gains `registry_path`, the Domain registry's repository path, when the
contract binds a Domain. A lane reads Domain structure from that file instead
of a tool call.

Under [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls), read-only Review keeps exact-subject HEAD/tree checks; repairing Review requires clean input B at entry and clean output C at terminal/verification, with diff reads pinned to B.

The packet carries prior work state as typed members, not as a tool read.
When `inputs.work_context` is present, the lane reads it first. The lane reads
each declared source through the host's packet-bound pinned
reader, not from the changed checkout. The worker supplies only its reading
ordinal, nonnegative byte offset, and a page length from 1 to 4096 bytes.
The host supplies the held packet and attempt identity. It uses the existing
committed-source proof, reads immutable Git bytes outside SQL, and revalidates
the active attempt and sampled source registrations before returning a page.
A page carries its source identity, whole-content hash, byte offsets, and EOF.
No caller-supplied path, ref, packet replacement, or mutable-checkout fallback
is admitted. A source read creates no workflow or evidence record.

An `inspect_diff` capability reads only the committed delta from the recorded
dispatch subject of the first editing attempt on the same claimed worktree
to the review attempt's pinned subject. Contract revisions do not reset this
foundation: Review inspects the accumulated solution under CD-0242 D5–D7.
The host verifies exact review HEAD
and a clean tree before access. The diff is restricted to the recorded
approved job's path scope. Missing base identity or scope refuses; the host
never guesses another base or widens to the whole repository.

The worker supplies only a nonnegative byte offset and a page length from
1 to 4096 bytes. The host supplies both raw commit identities, the held packet,
the attempt, and literal contained paths. It reads immutable Git objects
outside SQL and revalidates the active dispatch before returning a page.
A page carries both commit identities, whole-diff hash, byte offsets, and EOF.
No worker-supplied path, ref, packet replacement, or mutable-checkout fallback
is admitted. A diff read creates no workflow or evidence record.

When `inputs.checkpoint` is present, its diagnosis and strategy are
coordinator directions for the attempt.

Continuity and dispatch use the same explicitly declared immutable reading set.
A coordinator may declare a package owner note as an ordinary repository-file
reading when the task requires it. No global inventory inserts derived cards.
Repository source identity, declared reading bounds, findings, and context
frontier checks remain unchanged.

Required sources never fall back to changed checkout bytes. Domain IDs still
resolve against the Product registry and the approved contract's affected
scope. Recorded packets and reading sets stay readable under their recorded
formats as history. Access to general Concord state and workflow tools remains denied.

The core admission checks the declared context before any dispatch effect.
It observes the claimed worktree's clean `HEAD` against the admitted
`subject_commit` before the action transaction opens. The review lane's
`HEAD` and tree check against the packet subject stays the backstop for
drift after authorization. Every declared required reading resolves before
the lane spawns. Repository sources resolve at their pinned commits in their own
Project repositories. Law readings resolve through their registered source's
exact projected revision and canonical locator. No resolution falls back to
changed checkout bytes, rescans Git history, or repins a moved revision. The
packet's present `inputs.work_context`, `inputs.checkpoint`, and
`inputs.correction` members answer to the shared dispatch context bound
(CD-0016); an over-bound packet refuses whole and never truncates.

On an oracle-bound dispatch, `inputs.worker_job.acceptance_oracle` is the
one immutable oracle copy for every admitted lane. The shared work-context
reader projects canonical ranked findings and prior receipt references;
current blockers cannot disappear through context selection. Lanes read the
oracle and pinned sources, inspect its owners, and execute declared controls
within their permissions. Native-oracle teaching must distinguish supported
Go and target-repository drivers. The authenticated host owns qualified
preparation and execution under CD-0205 D1-D2; a lane gains no Concord tool
or native receipt authority from its language or ordinary command output.
Missing native readiness never selects an inspection-only acceptance route.
Independent review can report a legitimate omitted
path as `uncovered_case`. Earlier receipts remain regression baselines, not
current-subject acceptance. The authenticated host remains the sole qualified native-oracle producer.
A bounded check capability does not replace that producer or grant its receipt
authority to the worker. Missing context, readiness, or executable backing
still uses the report boundary, never a general worker Concord call.

## Alternatives considered

- Frontmatter denial alone. A denied utility lane still made Concord calls
  after its definition denied them, so prompt-level denial is not a proven
  sole control.
- Carrying the fetched state in the packet. Work history and other items'
  state are unbounded, and the packet already carries the approved objective,
  the design record, the law context, and the outcome predicates.
- A gate in the tool execution hook. A thrown hook error is not a structured
  envelope, and it carries no typed effect state the store records
  ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## Consequences

### Positive

- A lane starts from its packet, so no turn is spent on a refused call.
- The parent boundary does not trust the selected agent or its prompt, so the
  refusal holds even when a lane definition drifts.
- A new tool without an explicit lane-capability binding is denied at
  generation, not at recall.

### Cost

- A lane that genuinely needs recorded state needs a wider packet, and that
  widening is now a contract change.
- Every Concord call pays one host ancestry read. The read stays uncached by
  design, so an agent switch cannot outdate the boundary.

## Verification

- `python3 scripts/generate-agent-lanes.py --check` refuses drift in generated
  tool rules. Generator tests preserve blanket utility denials and default
  lane denials for tools without capability bindings.
- `bun test adapter/opencode/concord.test.ts` refuses a managed-parent call
  on read, transition, and work-start entries, asserts the structured
  envelope with no effect, proves the core never ran, and keeps a root
  coordinator session unchanged.
- `TestContinuityResolvesContractLawAndDomainContext` pins `registry_path` on
  a Domain-bound law context, and `TestContinuityLawOnlyContextCarriesNoRegistryPath`
  pins its absence when the contract binds no Domain.
- The live probe `opencode debug agent concord-implement` resolves the lane's
  rules and must show general Concord tools denied. Only explicitly bound
  capability tools remain eligible under the operator's host policy.
  `bun test adapter/opencode/packet.test.ts` pins the registry line rendered
  in the packet.
- `internal/store.TestValidateWorkContextDomainTxRefusals` and
  `internal/store.TestReadWorkContextOverflowRefusesReadNotRetention` prove
  source-qualified declared readings, findings, Domain scope, and bounds
  without an inventory or derived card references.
- `bun test adapter/opencode/packet.test.ts` preserves the declared shared
  context and rejects fields outside the current closed packet schema.
- `TestOwnerOracleImmutableProjection` and `TestOwnerOracleRepairFamily` cover the shared oracle and retained blockers and receipts.
- `TestReadWorkerReadingReturnsOnlyThePinnedDeclaredPage`,
  `TestReadWorkerReadingRefusesUnboundSelectorsBeforeGit`,
  `TestReadWorkerReadingRevalidatesRegistrationAfterGit`, and
  `TestReadWorkerReadingRevalidatesAttemptLivenessAfterGit` cover immutable
  source pages, bounds, authorization, and concurrent invalidation.
