# CD-0192: The refine pass proves a declared analysis ran

- **Status:** Accepted
- **Date:** 2026-09-29
- **Scope:** The refine exit of `workflow.implementation` and
  `workflow.break_fix`, the Project tooling manifest, and the argv equality
  the guard enforces
- **Amends:** CD-0138 D3 at its second sentence ("The step is agent-worked,
  not scanner-driven.")
- **Preserves:** CD-0138 D1, D2, and D4, the agent-judgment rule of CD-0138 D3,
  CD-0055 D4, CD-0096 D3, and every earlier definition version's behavior and
  digest
- **Related:** CD-0013, CD-0055, CD-0096, CD-0138, CD-0156
- **Approval:** The operator approved the work item contract that carries this
  record on 2026-09-29.

## Context

CD-0138 requires every code change to pass through the `refine` step before
its verdict step. The step demands an artifact evidence binding, and the bind
route checks only the evidence kind enum and the reference syntax. Research on
completed code-change items found that refine bindings name workflow operation
ids, attempt ids, or arbitrary strings almost every time. The declared tools
in a Project's `.concord/tooling.v1.json` were bound at refine zero times. The
pass could reach its exit without any analysis running on the change.

A declared check also cannot come from the change itself. A branch under
refine could add a trivial check to its own working tree and pass it. The
declaration a gate consumes must live where review owns it, on the Project's
default ref.

## Decision

### D1. The refine exit runs a guard at record_delivery

Leaving `refine` through `record_delivery` on `workflow.implementation`
version 18 and later, and `workflow.break_fix` version 16 and later, runs a
guard registered next to the delivery admission guards. The guard is active
only at the refinement step of those versions. Items pinned to earlier
definition versions keep the behavior and digest they shipped with.

### D2. The proof is a green worktree_verify run bound in the epoch

The guard admits the advance only when a verification evidence binding names a
completed `worktree.verify` durable operation for this work item. The named
run's lease must record outcome `completed`, exit code 0, and no tracked-file
change, and the lease acquire time must fall after the current refine start.
A replayed lease keeps its original acquire time, so a run from an earlier
refine epoch cannot prove this one. A binding that names a workflow operation
id, an attempt id, or any other string proves nothing.

### D3. The declared checks come from the default ref

When the work item's Project declares `.concord/tooling.v1.json` on its
default ref, the run's argv must equal the whitespace-split invocation of at
least one declared tool. The manifest is read with git from the default ref,
never from the working tree, and the read happens outside the store
transaction before the action's transaction opens. The tooling schema and
`scripts/check-project-tooling.py` forbid shell quoting and shell operators in
an invocation, so the whitespace split is the exact argv. A Project that
declares no manifest passes on any green run, and the refusal states that the
Project declares none. A Project that declares tools gets a refusal naming the
declared tool ids and invocations.

### D4. One declared check proves the pass

At least one declared check must match. Requiring every declared tool would
force a slow tier onto every small change, and a per-tier minimum needs its
own decision. The gate proves an analysis ran; strength grows as each Project
declares its checks.

## Alternatives considered

- Validate each `bind_evidence` call at refine. Rejected: a bind cannot decide
  whether refine is proved, because no route knows which binding is the final
  proof.
- Accept any verification reference. Rejected: that is the present gap.
- Require the run to name the current commit. Rejected: the lease row records
  no commit today, and adding one widens the change.
- Read the manifest from the working tree. Rejected: the change under refine
  could declare its own passing check.
- Add a separate argv array to the manifest schema. Rejected: two
  representations of one command drift.
- Refuse the refine exit until the Project adds a manifest. Rejected: only one
  Project declares a manifest today, so the gate would stall every new code
  change elsewhere.
- Skip the gate without a manifest. Rejected: it keeps the present gap.

## Consequences

Every code change on the current implementation and break-fix definitions
runs a green worktree_verify command inside the refine epoch and binds its
operation ref as verification evidence before the step exits. A Project
declares its checks once on the default ref, and every later change proves
itself against that declaration. Projects without a manifest still run a real
green check, which is strictly stronger than the unchecked artifact the
previous rule accepted.

The gate reads the manifest outside the store transaction, so a slow git read
never holds the store's single connection. A missing manifest at the default
ref and a Project without a registered repository both read as no
declaration. An unreachable repository refuses the exit until the read
succeeds.

## Verification

- `go test ./internal/store/ -run TestRefineExitRefuses` proves the unbound
  exit refuses, a binding that names no verify run proves nothing, a run
  acquired before the refine start stays stale, and a green run in the epoch
  admits the exit on both families.
- `go test ./internal/store/ -run 'TestDeclaredToolInvocation|TestProjectWithoutManifest'`
  proves the declared split admits the exit, an undeclared argv refuses with
  the declared checks named, and a Project without a manifest passes on any
  green run and refuses without one.
- `go test ./internal/store/ -run TestRefineToolingResolution` proves the
  resolution reads the default ref only and reads an absent manifest as no
  declaration.
- `go test ./internal/store/ -run TestWorkflowDefinitionVersionPinsHold`
  proves every earlier definition digest is unchanged, and the new versions
  hold their own pins.
- `python3 scripts/test-project-tooling.py` proves the checker and the schema
  refuse shell quoting and shell operators in an invocation.
- The workflow corpus carries `WF57-refine-exit-needs-green-verify-run`, whose
  `record_delivery` at a started refine step refuses with the missing-evidence
  refusal.
- `python3 scripts/check-doc-contract.py` proves this record carries the
  current decision outline and passes the writing rules.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove this record registers
  with a current content hash and no unprocessed document remains.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0192
  identifier allocates once.
