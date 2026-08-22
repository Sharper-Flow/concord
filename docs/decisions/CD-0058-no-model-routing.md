# CD-0058: Concord performs no model routing

- **Status:** Accepted
- **Date:** 2026-08-22
- **Scope:** Model resolution authority; worker-attempt model evidence; the
  routing-policy record and its load path; issue #338
- **Approval:** Operator accepted the drafted decision as written on 2026-08-22;
  the public record is an
  [issue #338 comment](https://github.com/Sharper-Flow/concord/issues/338)
- **Related:** CD-0054 (supersedes its D1 load path, D2 validation, and D5
  template clauses), CD-0017 (amends D2's dispatch flag and retires D8/D9;
  D6 distinctness is preserved unchanged), CD-0036 (breaking cutover),
  CD-0044 (evidence boundary, unaffected)
- **Preserves:** reviewer/implementer model distinctness, worker-attempt model
  evidence, cost attribution
- **Supersedes:** CD-0054's rejected alternative "drop the resolution set
  entirely, trust readback"; nothing else

## Context

CD-0054 moved routing-policy authority from compile time to load time and left
the embedded default naming one installation's providers. Because CD-0054 D2
validates every declared identifier against `opencode models`, a host without
`openai/gpt-5.6-luna`, `zai-coding-plan/glm-5.3`, and `kimi-for-coding/k3`
fails closed on first dispatch with `routing_policy_model_unavailable`. The
operator must author a four-class policy JSON before any lane runs. CD-0054
relocated the installability defect rather than removing it.

The prior decision recorded what the resolution set buys: "not the choice of
model — the host already owns that — but the anti-silent-substitution bound and
the pre-declared identity of the executing model." That claim was not retested
against the code.

Only one mechanism consumes the declared side. `ValidateWorkerCompletion`
(`internal/store/worker_lanes.go:383`) fails with `KindModelIdentityMismatch`
when `resolved_model` differs from `readback_model`. It is meaningful solely
because the adapter asserts an intent by passing `--model`. Withdraw the
assertion and the check is not weakened, it is vacuous: no intent remains to
violate.

The feature that genuinely needs model identity does not read the declared
side at all. `internal/store/schema.go` records that "CD-0017 D6 evaluates
distinctness against readback executing-model identity." Distinctness, cost
attribution, and an accurate account of what executed all rest on
`readback_model`.

Two resolution paths exist today and disagree. The adapter passes `--model`;
a Task-tool spawn passes nothing and inherits its parent, because CD-0054 D3
correctly removed `model:` from the generated lane definitions. They disagree
precisely because one asserts a model. Withdrawing the assertion collapses them
into a single story.

## Decision

### D1. Concord performs no model resolution

No Concord code path selects, declares, ranks, or validates a model identifier.
The adapter's lane spawn omits `--model`. OpenCode resolves the executing model
from host configuration — `agent.<name>.model`, which the operator's
model-routing plugin already writes — exactly as it resolves any other subagent.

This completes the direction CD-0054 began. Models are per-host configuration;
Concord holds no second opinion about them.

### D2. `readback_model` is the sole model evidence

A worker attempt records the model the host reports as having executed it.
That value remains the input to CD-0017 D6 distinctness, unchanged. Concord
asserts nothing about which model *should* have run, so it detects no
substitution and claims none.

### D3. The routing-policy record and its load path are removed

`contracts/routing-policy.v1.json`, the `CONCORD_ROUTING_POLICY` environment
override, the embedded default, load-time validation, and the
`opencode models` existence check are deleted. The repository retains no model
identifier outside test and eval fixtures.

### D4. The declared-side attempt columns are dropped

`worker_attempts.routing_policy_version`, `routing_policy_digest`,
`resolved_model`, `resolution_role`, and `fallback_reason` are dropped under a
schema migration. All five are `NOT NULL` (`internal/store/schema.go:1362-1405`),
so retention would require sentinel values describing nothing. A column that
records a decision the system no longer makes is not evidence.

This is a breaking cutover under CD-0036.

### D5. Capability classes remain as model-neutral labels

Lanes continue to declare a capability class per CD-0054 D3. With no resolver
the class carries no runtime effect; it documents lane intent and tells an
operator which host agent entries warrant configuration. It is retained as
documentation, not as a routing input.

### D6. CD-0017 clauses amend as follows

- D2's dispatch mechanism drops `--model`; the adapter continues to validate
  envelopes and identity only.
- D8 and D9 are retired. Concord declares no legal resolution set, so there is
  no boundary between host fallback mechanics and Concord-owned legality.
- D6 distinctness is untouched and continues to evaluate readback identity.

## Consequences

- A single-model installation configures nothing and dispatches successfully.
- An operator wanting per-lane routing configures it once, in the plugin that
  already owns per-agent model selection.
- Silent-substitution detection is withdrawn. Concord records what executed and
  makes no claim about what was permitted to execute. This is the deliberate
  cost of D1 and the reason CD-0054's rejected alternative is superseded rather
  than merely amended.
- `#251` and `#252` close as obsolete; both concern a record that no longer
  exists.
- `check-lane-evals.py` stops reading preferred models by capability class.

## Rejected alternatives

**Derive the resolution set from host routing-plugin configuration.** Rejected:
a bound derived from the config that drives the routing it audits is very
nearly tautological, and it keeps Concord in the routing business while
appearing not to be. It also couples a Concord invariant to a third-party
schema.

**Emit `model:` frontmatter into the generated lane definitions.** Rejected:
CD-0054 D3 removed exactly that, and generation reads the repository template
rather than host state, so every installation would inherit one operator's
credentials.

**Retain the columns as nullable.** Rejected: all five are `NOT NULL`, so this
is a migration either way, and the nullable form preserves a schema shape that
asserts a decision Concord no longer makes.

## Verification

- A host declaring one model, with no routing-policy file and no per-agent
  plugin entry, dispatches a lane attempt end to end.
- No Concord source path references a model identifier outside test and eval
  fixtures.
- `readback_model` is recorded for every attempt; a CD-0017 D6 distinctness
  test passes unchanged against readback values.
- The migration drops the five columns and existing rows survive it.
- `scripts/check-agent-contracts.py` and `scripts/check-json.py` pass with
  regenerated outputs.
