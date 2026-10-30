# CD-0220: Repository authoring follows lane capability and approved scope

- **Status:** Accepted
- **Date:** 2026-10-10
- **Scope:** Authored and generated files under `.concord/`; worker edit authority and its enforcement limits.
- **Time zone:** Eastern Daylight Time (EDT)
- **Approval:** Snowball coordinator, operator-delegated, 2026-10-10 01:08 EDT
- **D5 Code Mode approval:** [Operator, 2026-10-28](https://linear.app/sharper-flow/issue/CON-1307#comment-875632b6-c2e1-4f57-9295-bd265f31f620).
- **Related:** [CD-0006](CD-0006-concord-root-product-policy.md), [CD-0036](CD-0036-breaking-law-cutovers.md), [CD-0072](CD-0072-unguarded-trunk-file-writes.md), [CD-0159](CD-0159-authority-tier-for-product-law.md), and [development authority](../managed-development-authority.md).
- **Preserves:** Product-law approval, revision identity, cutover, host ownership, and the existing lane capability registry.

## Context

The `.concord/` directory contains authored Product law, knowledge metadata, configuration, and generated projections.
A path alone does not determine edit authority or law status.
The lane registry grants source edits through `edit_scoped_files`; role and job
scope delimit their use.
Product-law approval and permission to write a file are different facts.

## Decision

### D1. The coordinator owns the approval decision, not every file edit

The coordinator may draft law during shaping and integrate an authorized repository change.
An execution-time law conflict returns to the existing planning or contract-revision boundary before further affected execution.
CD-0006, CD-0159, and CD-0036 continue to determine approval, authority tier, revision identity, and breaking cutover.
A draft, commit, generated hash, or worker report does not enact or promote law.

### D2. Editing lanes may deliver only their approved source changes

A lane with `edit_scoped_files` may author `.concord/` sources within its approved job scope and dispatched worktree.
The existing dispatch registry admits implement and design only at `external_effect` steps.
Their assigned role purpose remains binding; directory location does not expand that purpose.
A law addition or amendment requires the approved contract's `added` or `modified` binding for that law.
An existing law's `mandated` or `obligation` role alone grants no edit authority.
The coordinator resolves missing scope or law conflicts; a worker does not expand its own authority.

### D3. Non-editing lanes report; they do not author repository sources

A lane without `edit_scoped_files` must not create, change, delete, or commit repository sources; for the review lane,
review inspects the accumulated solution, may commit corrective changes, verifies the corrected candidate, and reports ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).
This rule includes law, specifications, knowledge metadata, and configuration under `.concord/`.
Research and verify return their assigned reports and evidence instead.
Generated definitions deny native shell and named editor tools for lanes
without `edit_scoped_files`. The generator derives those denials from the
existing capability registry, not a second writer list.

### D4. Command artifacts do not confer authoring authority

A lane may run only commands its role and job permit.
Those commands may produce normal test artifacts or regenerate projections from authorized inputs.
Generated projections retain their owning generators and must not be hand-edited.
Changing an input, record status, or authority tier is source authoring, not regeneration.
Refreshing a content hash or canonical encoding does not approve the content or its evidence.

A non-editing lane executes checks only through a packet-bound host capability.
The worker selects a recorded executable control, never an argv, shell string,
repository path, or replacement recipe. The host validates the active attempt
and uses the existing execution and lease owner for that control.

A job's ordinary `checks` strings state requirements, not executable authority.
An unbacked requirement remains required. The lane reports the missing backing
and returns a failed status when that gap blocks its assigned result. The
coordinator supplies approved executable backing; the worker does not infer it.

### D5. Enforcement is layered and its limits remain explicit

Dispatch admission enforces the lane's permitted step kind. Generated host
permissions deny native shell and named editors for non-editing lanes. They
never override an operator's narrower host policy with a positive tool grant.

Before a dispatched worker uses a native or directly exposed MCP tool, the
adapter authenticates the host child, its authorized opening packet, its
active attempt, and its dispatched worktree. One attempt binds one host child.
The registered lane capabilities determine a closed tool allowlist. Unknown
tools refuse before execution. A selected-agent change cannot widen that list.

Code Mode `execute` is admitted only on a host path for which native execution
evidence proves that every nested MCP call reaches the same capability ceiling
before execution. The adapter refuses `execute` on an unqualified or unknown
running host version. Enabling `execute` grants no additional MCP capability.
Direct local repository MCP tools must target the dispatched repository.
A settling or retired attempt grants no further worker calls.

Independent review examines the actual Git delta, approved scope, and
law-approval evidence before acceptance and merge. Document, knowledge, and
generator checks validate artifacts; they do not prove who wrote or approved
them.

These controls do not establish a filesystem or network sandbox, prevent an
authorized editing lane's arbitrary shell writes, or attribute writes in a
shared worktree. Native read and public-source tools retain their host resource
permissions. No per-path filesystem write interceptor is introduced.
CD-0072 remains in force. Outside-workflow host actors keep D6's boundary.

### D6. Outside-workflow changes retain their existing authority boundary

An authorized host actor may perform repository work outside Concord workflow under host permissions and repository rules.
Product-law approval and independent repository review remain required where applicable.
That route has no Concord workflow step and creates no managed dispatch, verdict, or completion evidence.
Concord does not change host roles or claim that generated lane permissions govern such actors.

## Alternatives considered

- Coordinator-only authoring narrows editing-lane delivery and moves source integration into coordinator context without preventing shell writes.
- A separate law-authoring lane duplicates the existing capability and job-scope owners.
- Review alone leaves non-editing lanes with direct editors despite their declared role.
- A post-worker diff cannot reliably identify a writer in a shared worktree without a new concurrency and attribution protocol.
- Path-based file-write interception changes the accepted trunk enforcement
  boundary. It is not required for tool-capability admission.

## Consequences

Implement and design retain approved source authoring, including explicitly bound law changes.
Research, review, and verify lose direct editor access but retain permitted tests, validators, and report output.
Host permissions remain operator-owned. Capability admission limits tool
classes and the named local MCP target; it is not a complete resource sandbox.
No additional approval turn is imposed on derived-law revisions already approved within the contract under CD-0159.
The separate committed-content rule determines which content belongs in the repository; this decision determines authoring authority.
Host permission resolution and independent review remain separate evidence obligations, not conclusions from artifact checks.

## Verification

- `python3 scripts/test-generate-agent-lanes.py` checks capability-derived direct-editor denials for non-editing lanes and preserves editing-lane law bindings.
- `python3 scripts/generate-agent-lanes.py --check` detects generated permission or instruction drift.
- `python3 scripts/check-knowledge-index.py` checks record identity and content proofs; independent review checks approval and the actual source delta.
- `bun test adapter/opencode/lane_capabilities.test.ts` exercises direct tool
  denials, worktree targeting, opening-packet identity, single-child binding,
  agent changes, attempt retirement, and refusal of Code Mode on unqualified
  running hosts. `bun run adapter/opencode/lane_capabilities.native.case.ts`
  exercises the real host's Task children and nested MCP hook path on the
  qualified host version.
  It proves permitted repository and public-source calls reach transport while
  foreign-repository calls and tools outside the lane's capabilities do not.
  Its core authorization is a fixture; it proves no Concord workflow result.
- `go test ./internal/agent -run '^TestWorkerControl'` exercises the shared
  signed assertion framing, actual-child policy, revocation, and Product scope.
- `go test ./internal/store -run '^(TestNativeWorkerControl|TestResolveWorkerControl)'`
  exercises recorded control resolution, live-attempt joins, child attribution,
  nonce/lease atomicity, and authorized replay through the existing producer.
  No ordinary check string may become executable authority.
