# CD-0196: Worker lanes hold no Concord tool access, and the dispatch packet is their complete Concord context

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** The tool access of dispatched worker lanes, the adapter surface
  those lanes reach, and the packet block that carries law and Domains to a
  lane; [Concord (CON) issue 746](https://linear.app/sharper-flow/issue/CON-746)
- **Amends:** CD-0017 D4 at its paragraph ("Workers never record step
  transitions, verdicts, or completion, and never spawn nested workflow
  authority."), adding the tool-access boundary the same boundary implies
- **Preserves:** CD-0017 D1 to D9 at every other sentence, CD-0005 D6, and the
  coordinator routes every lane's parent already uses
- **Related:** CD-0017, CD-0020, CD-0043, CD-0102, CD-0193
- **Approval:** The operator accepted the work item CON-746 contract that
  carries this record on 2026-09-30.

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

A dispatched worker lane holds no Concord read or write tool access. The
dispatch packet is the lane's complete Concord context. Three mechanisms
carry the boundary, and each one holds without the other two.

1. Every generated lane and utility definition denies every `concord_*` tool
   in its frontmatter. The generator derives the identifier list from the
   agent and host tool-surface contracts, so a new Concord tool is denied for
   every lane when the contracts regenerate.

2. Every `concord_*` tool entry in the adapter checks the caller before the
   core is invoked. A session with a managed parent is a dispatched lane, and
   a scope the host cannot resolve fails closed. Both receive a structured
   refusal with no effect that names CD-0017 D4. A root coordinator session
   passes unchanged.

3. Every lane definition states the boundary: the packet is complete, Concord
   tools are unavailable, and law is read from the repository paths the
   packet names. A lane reports missing context in its evidence, and returns
   a failed status when missing context blocks the assigned result.

The packet carries the one fact lanes still fetched. The workflow law context
gains `registry_path`, the Domain registry's repository path, when the
contract binds a Domain. A lane reads Domain structure from that file instead
of a tool call.

The packet carries prior work state as typed members, not as a tool read.
When `inputs.work_context` is present, the lane reads it first. The lane reads
each repository source at its pinned commit through Git, not from the changed
checkout. When `inputs.checkpoint` is present, its diagnosis and strategy are
coordinator directions for the attempt.

When a context subject's repository adopts complete Domain navigation, the
shared reader supplies its affected-Domain card references and its root card
once. Continuity and dispatch use the same derived reading set.
Each card reference names its Project, repository-relative path, and applicable
commit OID, with the validated Domain ID. The inventory resolves the card path.
Registered repositories use their own cards and the shared registry's identities.

Existing source identity supplies deduplication and revision proof.
Card content stays outside the packet. Required cards count against the existing
reading-entry bound, not a second budget. Missing objects, stale bindings, or
overflow refuse before dispatch. The reader never substitutes changed checkout
bytes or silently drops a required source. Older unadopted reading sets remain
readable under their recorded format. Worker tool access remains unchanged.

## Alternatives considered

- Frontmatter denial alone. A denied utility lane still made Concord calls
  after its definition denied them, so prompt-level denial is not a proven
  sole control.
- Carrying the fetched state in the packet. Work history and other items'
  state are unbounded, and the packet already carries the approved objective,
  the design record, the law block, and the outcome predicates.
- A gate in the tool execution hook. A thrown hook error is not a structured
  envelope, and it carries no effect state the core can fold.

## Consequences

### Positive

- A lane starts from its packet, so no turn is spent on a refused call.
- The parent boundary does not trust the selected agent or its prompt, so the
  refusal holds even when a lane definition drifts.
- A new Concord tool is denied for every lane at generation, not at recall.

### Cost

- A lane that genuinely needs recorded state needs a wider packet, and that
  widening is now a contract change.
- Every Concord call pays one host ancestry read. The read stays uncached by
  design, so an agent switch cannot outdate the boundary.

## Verification

- `python3 scripts/generate-agent-lanes.py --check` refuses a lane definition
  that lacks a deny entry for any surface tool, and the regenerated
  definitions deny all eleven `concord_*` tools.
- `bun test adapter/opencode/concord.test.ts` refuses a managed-parent call
  on read, transition, and work-start entries, asserts the structured
  envelope with no effect, proves the core never ran, and keeps a root
  coordinator session unchanged.
- `TestContinuityResolvesContractLawAndDomainContext` pins `registry_path` on
  a Domain-bound law context, and `TestContinuityLawOnlyContextCarriesNoRegistryPath`
  pins its absence when the contract binds no Domain.
- The live probe `opencode debug agent concord-implement` resolves the lane's
  tool rules and shows every `concord_*` tool denied, and
  `bun test adapter/opencode/packet.test.ts` pins the registry line the
  packet renders.
- `go test ./internal/store -run WorkContextNavigation` checks pinned card
  references through the shared reader, existing-source deduplication,
  registered sources, typed refusals, and the existing reading bound.
- `bun test adapter/opencode/packet.test.ts` checks that packets preserve pinned
  card references without card content.
