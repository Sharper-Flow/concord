# CD-0058: Concord performs no model routing

- **Status:** Accepted
- **Date:** 2026-08-22
- **Scope:** Model resolution authority; worker-attempt model evidence; the
  routing-policy record and its load path; issue #338
- **Approval:** Operator accepted the drafted decision as written on 2026-08-22;
  the public record is an
  [issue #338 comment](https://github.com/Sharper-Flow/concord/issues/338)
- **Related:** CD-0054 (model-neutral registry), CD-0017 (worker evidence and
  workflow-declared distinctness), CD-0036 (breaking cutover),
  CD-0044 (evidence boundary, unaffected)
- **Preserves:** reviewer/implementer model distinctness, worker-attempt model
  evidence, cost attribution

## Context

Models are per-host configuration. A Product-owned resolution set would require
each installation to declare accessible providers before a lane could run.
Concord's worker contract needs the executing identity, not a second model
resolver. Distinctness, cost attribution, and an accurate account of execution
rest on `readback_model`.

A model-substitution check needs a declared intent to compare with execution.
Concord asserts no model intent, so readback proves what executed without a
substitution guarantee.

## Decision

### D1. Concord performs no model resolution

No Concord code path selects, declares, ranks, or validates a model identifier.
The adapter's lane spawn omits `--model`. OpenCode resolves the executing model
from host configuration — `agent.<name>.model`, which the operator's
model-routing plugin already writes — exactly as it resolves any other subagent.

Models are per-host configuration; Concord holds no second opinion about them.

### D2. `readback_model` is the sole model evidence

A worker attempt records the model the host reports as having executed it.
That value remains the input to CD-0017 D6 distinctness, unchanged. Concord
asserts nothing about which model *should* have run, so it detects no
substitution and claims none.

Because `readback_model` is the sole
evidence, its absence is itself recorded. A host export that carries no model
identity, or more than one, produces one attempt born `failed` in one event,
with kind `model_readback_missing` or `model_readback_ambiguous` (CD-0017 D5).
The adapter never retries the export and never leaves a dispatch with no
attempt row.

### D3. The routing-policy record and its load path are removed

`contracts/routing-policy.v1.json`, the `CONCORD_ROUTING_POLICY` environment
override, the embedded default, load-time validation, and the
`opencode models` existence check are deleted. The repository retains no model
identifier outside test and eval fixtures.

### D4. The declared-side attempt columns are dropped

`worker_attempts.routing_policy_version`, `routing_policy_digest`,
`resolved_model`, `resolution_role`, and `fallback_reason` are absent from the
worker-attempt schema. Migration 44 removes the five `NOT NULL` declared-side
columns and preserves existing rows. Retention would require sentinel values
describing nothing. A column that records a decision the system does not make
is not evidence.

This is a breaking cutover under CD-0036.

### D5. Capability classes remain as model-neutral labels

Lanes continue to declare a capability class per CD-0054 D3. With no resolver
the class carries no runtime effect; it documents lane intent and tells an
operator which host agent entries warrant configuration. It is retained as
documentation, not as a routing input.

## Consequences

- A single-model installation configures nothing and dispatches successfully.
- An operator wanting per-lane routing configures it once, in the plugin that
  already owns per-agent model selection.
- Concord records what executed and makes no claim about what was permitted to
  execute. The absence of substitution detection is the deliberate cost of D1.

## Rejected alternatives

**Derive the resolution set from host routing-plugin configuration.** Rejected:
a bound derived from the config that drives the routing it audits is very
nearly tautological, and it keeps Concord in the routing business while
appearing not to be. It also couples a Concord invariant to a third-party
schema.

**Emit `model:` frontmatter into the generated lane definitions.** Rejected:
a repository model pin would make every installation inherit one operator's
model access. CD-0054 D3 keeps the registry model-neutral.

**Retain the columns as nullable.** Rejected: all five are `NOT NULL`, so this
is a migration either way, and the nullable form preserves a schema shape that
asserts a decision Concord no longer makes.

## Verification

- A host declaring one model, with no routing-policy file and no per-agent
  plugin entry, dispatches a lane attempt end to end.
- No Concord source path references a model identifier outside test and eval
  fixtures.
- `internal/store.TestWorkerModelReadbackFailureIsDurableWithoutModelValue`
  proves a failed attempt retains missing-readback evidence without a model value.
- `adapter/opencode/dispatch.test.ts` checks that lane dispatch omits `--model`
  and that missing or ambiguous readback records one failed attempt.
- `internal/store.TestDeclaredModelDistinctnessRejectsCollisionAndFailsClosed`
  proves workflow-declared distinctness evaluates executing-model evidence.
- `internal/store.TestMigrateV43ToV44DropsWorkerRoutingEvidenceAndPreservesRows`
  proves the migration removes the five columns and preserves existing rows.
- `scripts/check-agent-contracts.py` and `scripts/check-json.py` pass with
  regenerated outputs.
