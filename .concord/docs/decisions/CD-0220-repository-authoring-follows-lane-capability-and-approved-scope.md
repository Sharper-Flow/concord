# CD-0220: Repository authoring follows lane capability and approved scope

- **Status:** Accepted
- **Date:** 2026-10-10
- **Scope:** Authored and generated files under `.concord/`; worker edit authority and its enforcement limits.
- **Time zone:** Eastern Daylight Time (EDT)
- **Approval:** Snowball coordinator, operator-delegated, 2026-10-10 01:08 EDT
- **Related:** [CD-0006](CD-0006-concord-root-product-policy.md), [CD-0036](CD-0036-breaking-law-cutovers.md), [CD-0072](CD-0072-unguarded-trunk-file-writes.md), [CD-0159](CD-0159-authority-tier-for-product-law.md), and [development authority](../development-authority.md).
- **Preserves:** Product-law approval, revision identity, cutover, host ownership, and the existing lane capability registry.

## Context

The `.concord/` directory contains authored Product law, knowledge metadata, coverage declarations, configuration, and generated projections.
A path alone does not determine edit authority or law status.
The lane registry grants scoped source edits to implement and design, but non-editing lanes inherit direct editor access from the host.
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

A lane without `edit_scoped_files` must not create, change, delete, or commit repository sources.
This rule includes law, specifications, knowledge metadata, coverage declarations, and configuration under `.concord/`.
Research, review, and verify return their assigned reports and evidence instead.
Their generated definitions deny native file-edit permission and the separate `morph_edit` tool.
The generator derives these denials from the existing capability registry, not a second writer list.

### D4. Command artifacts do not confer authoring authority

A lane may run only commands its role and job permit.
Those commands may produce normal test artifacts or regenerate projections from authorized inputs.
Generated projections retain their owning generators and must not be hand-edited.
Changing an input, record status, authority tier, or coverage claim is source authoring, not regeneration.
Refreshing a content hash or canonical encoding does not approve the content or its evidence.

### D5. Enforcement is layered and its limits remain explicit

Dispatch admission enforces the lane's permitted step kind.
Generated host permissions deny named direct edit tools for non-editing lanes.
Independent review examines the actual Git delta, approved scope, and law-approval evidence before acceptance and merge.
Document, knowledge, and generator checks validate artifacts; they do not prove who wrote them or approved them.
These controls do not prevent arbitrary shell writes or establish per-worker attribution in a shared worktree.
No editor-host write-interception hook or filesystem sandbox is introduced; CD-0072 remains in force.

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
- An editor-host write-interception hook conflicts with CD-0072.

## Consequences

Implement and design retain approved source authoring, including explicitly bound law changes.
Research, review, and verify lose direct editor access but retain permitted tests, validators, and report output.
Shell and host-added tool permissions remain host-owned; generated editor denial is not a complete write sandbox.
No additional approval turn is imposed on derived-law revisions already approved within the contract under CD-0159.
The separate committed-content rule determines which content belongs in the repository; this decision determines authoring authority.
Host permission resolution and independent review remain separate evidence obligations, not conclusions from artifact checks.

## Verification

- `python3 scripts/test-generate-agent-lanes.py` checks capability-derived direct-editor denials for non-editing lanes and preserves editing-lane law bindings.
- `python3 scripts/generate-agent-lanes.py --check` detects generated permission or instruction drift.
- `python3 scripts/check-doc-contract.py` checks the decision outline and prose; it does not prove author approval.
- `python3 scripts/check-knowledge-closure.py --strict` checks registration closure; it does not prove author approval.
- `python3 scripts/check-knowledge-index.py` checks record identity and content proofs; independent review checks approval and the actual source delta.
