# CD-0128: The design record travels with the implement packet

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** Typed design, architecture revisions, and bounded implementation slices in implementation and break-fix workflows
- **Approval:** The operator approved this bounded change in Concord work-42d1699f7c11fa7e6341b607.
- **Related:** CD-0013, CD-0016, CD-0043, CD-0067, and CD-0115
- **Preserves:** CD-0043 D1. Lane methodology remains host-owned and is not Concord durable state.

## Context

The implementation workflow has a design step, but `record_design` carried no typed record into later execution. A worker could receive the approved objective without the decisions that selected its approach.

## Decision

`record_design` contains an approach, one to sixteen typed decisions, and
one to sixty-four touched references.
Architecture-capable implementation and break-fix definitions also require a
closed architecture record before their existing contract approval and first
editing dispatch. Earlier pins retain their recorded design shape and graph.

The coordinator authors the architecture, not an editing lane. It declares
owners by Project, contained path, and symbol; invariants; data flow; failure
behavior; pinned reference-code paths and line ranges; unchanged files; and
ordered slices. Each slice names one owner, an exact file set, test controls,
an end state, and a changed-line limit no greater than 500. The approved file
map identifies source, test, and wholly generated files. Mixed files count as
source. The architecture is bounded typed content, not a Markdown path or a
worker-supplied scope assertion.

The continuity projection exposes the latest current design record. A contract
supersession invalidates design records that precede it in the applied work
version sequence. Those records remain in audit history, but continuity does not
present them as current. The packet carries the current record as the typed
`inputs.design_record` member. The packet carries the resolved law context as
`inputs.law_context`, the recorded proposal as `inputs.proposal_record`, and
the work item's recorded value statement, task, and narrative as
`inputs.work_record`. The adapter copies each member verbatim and writes no
prose around it. The closed packet schema bounds each member, and the adapter
refuses a packet outside those bounds.

The packet also carries the pinned work-context view of CD-0016 as
`inputs.work_context` and the latest context checkpoint as `inputs.checkpoint`.
The adapter copies both verbatim. The dispatch transaction re-reads these two
members and the four record members above. It refuses a packet whose member
differs from the current state, omits a present member, or carries a member
that no record backs. The core refuses the prose `inputs.context` member on
dispatch. The lane packet schema keeps that member only so that recovery can
validate a packet that an earlier adapter sent.

The record describes the change's decisions. It does not describe lane procedure, review method, or verification method. Research inputs use the existing `research_bindings` field with `use_role=design_input`.

The forward-only graph places the initial design before planning and execution.
After contract correction, worker dispatch requires a current design when the
work has an earlier typed design. The work pin omits dispatch until this condition
holds. Workflows without a typed design remain unchanged.

An operator-approved `supersede_contract` may include a replacement `design_record`
with the same bounded content as `record_design`. Contract supersession and the
replacement record commit in one transaction. This route does not admit the
initial design action at a later step or change a pinned definition. The
bounded revision route below supplies later revisions on new pins only.

### Architecture approval and bounded revision

The existing contract transaction binds the current design revision and
digest, architecture, and bounded revision policy. Legacy policy retains its
exact operator approval. Under `approval_policy=consequence_v1`, engineering
records the same binding through its authenticated coordinator without a
consent prompt. Decision-bearing consent follows CD-0013 D11. Product-law
Domain bindings retain their separate meaning.
The consequence_v1 clauses apply only after that policy is enacted in
CD-0013 D11 and declared by the pinned definition. Otherwise legacy policy
remains in force; a citation alone never activates a consent policy.

`revise_design` is routine under consequence_v1 and requires no operator
approval under legacy policy. The contract binds its bounded revision policy;
no synthetic operator, approval token, or extra question is required.

`revise_design` appends a design revision and holds the current step. It
requires the expected design revision, a reason, replacement typed content,
and evidence references. All worker attempts must be terminal. The approved
owners, invariant statements, file-role map, unchanged files, and parent
contract scope remain fixed. The unstarted suffix may be repartitioned,
reordered, or extended within that envelope, including integration and
collision repair before completion. An accepted prefix remains immutable.
An unresolved dispatched obligation cannot be renamed, weakened, or split
away. Wider changes use existing contract correction.

A revision changes no contract version, step start, attempt epoch, or retry
window. Prospective jobs bound to an older revision become unready. Historical
records, packets, and results remain readable without invented architecture.
The core checks structural joins; independent review judges semantic adequacy.

The packet carries the architecture once inside its typed design record. The
job binding selects the slice whose section, allowlist, limit, and references
the lane follows. Missing, stale, substituted, or over-bound content refuses
before dispatch. No checkpoint or task prose can replace the recorded slice.

The serialized architecture is at most 8192 UTF-8 bytes. A closed capacity
allocation bounds the complete packet, including all other members, to
32768 serialized bytes. The non-packet mutation frame reserves at most
16384 bytes, so even a retry response with its packet stays below 51200.
The core checks worst-case reachable projections and current complete inputs
at design admission. All relevant local event and native writers preserve
the allocation before commitment. Quiescent revision may reallocate space,
but cannot drop an obligation, truncate a record, or exceed the total.
Continuity uses its lossless paged transport and unchanged envelope limit.

## Consequences

Implementation workers receive the coordinator's recorded approach and decisions without making an architecture choice. Released workflow definitions remain digest-pinned, and the prior implementation version keeps its generic design action.

Architecture-capable break-fix definitions add an internal design step before
their existing approval checkpoint. No new human checkpoint is added. Earlier
pins and generic one-off retain their existing graphs. Released definition
content and historical packet formats never change in place.

## Verification

- The internal store tests pass.
- The adapter packet tests pass, including design presence, design absence, and context overflow.
- Agent contracts regenerate without drift.
- Public correction tests cover stale-design refusal, atomic replacement,
  approval binding, and restored dispatch admission.
- Event-rebuild tests preserve invalidation, the replacement record, and history.
