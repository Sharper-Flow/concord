# CD-0054: Routing-policy authority is load-time host state

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** The routing-policy record's authority location; lane-registry
  model neutrality; issue #291
- **Approval:** Operator accepted the drafted decision as written on 2026-08-21; the
  public record is
  [issue #291 comment](https://github.com/Sharper-Flow/concord/issues/291#issuecomment-5375408050)
- **Related:** CD-0017 (amends D2, D3, D9, Invariant 5), CD-0043 (amends the
  D1 "which model runs it" clause), CD-0034 (host-provenance regime),
  CD-0044 (evidence boundary), CD-0049 (project-local lane delivery)
- **Preserves:** the evidence boundary in full — per-attempt routing-policy
  version and digest, `resolved_model`, readback verification, typed fallback
  reasons, undeclared-resolution terminality
- **Supersedes:** the authority-location clauses of CD-0017 D3/D8/D9 and
  Invariant 5; nothing else

## Context

`contracts/routing-policy.v1.json` pinned one operator's provider access as
digest-pinned public law. `openai/gpt-5.6-luna`, `zai-coding-plan/glm-5.3`,
`kimi-for-coding/k3` are this installation's credentials, not Product truth.
Any other install inherited a contract declaring models it cannot reach — an
installability defect for a public project.

The operator direction (issue #291): models are per-host configuration; each
user declares what they have access to. The load-bearing question was what the
repo pin actually bought. The answer: not the choice of model — the host
already owns that, via readback-verified resolution inside a declared set —
but the anti-silent-substitution bound and the pre-declared identity of the
executing model.

The evidence schema was already ready: `worker_attempts` has carried
per-attempt `routing_policy_version` + `routing_policy_digest` since v25, and
dispatch validation is policy-content-agnostic. The single hard wall was the
compile-time generated constant in `internal/store/routing_policy.go` — the
repo contract was authoritative only because it was the only source the store
would load.

## Decision

### D1. Authority moves from compile time to load time

The store and the adapter resolve the routing policy at process start:
`CONCORD_ROUTING_POLICY` names an absolute path to a host policy JSON (same
schema as the repo record). A set-but-unreadable or invalid path fails closed
with a typed failure naming the path and field — never a silent fallback.
Unset, the embedded default — the repo's `contracts/routing-policy.v1.json`
content — remains the template every installation can start from. The digest
is computed over the loaded content at load, and every existing
digest-binding surface (dispatch lookup, signed assertions, worker-attempt
evidence) binds the loaded digest unchanged.

### D2. Load-time validation replaces cross-file pinning

The old cross-anchors (policy preferred == lane `pinned_model`; generator
cross-check) become load-time checks: every capability class in the lane
registry is covered exactly once; resolution sets are non-empty, duplicate
free, and led by the preferred model; shape is strict (unknown fields and
trailing bytes rejected). The adapter additionally validates — on the host,
where `opencode` exists — that every declared identifier appears in
`opencode models`, failing typed with the offending names. This is the
strongest form of issue #251: existence is checked against the host's real
providers at declaration time, not a CI string shape-check.

### D3. The lane registry is model-neutral

`pinned_model` leaves the lane registry, its schema, the generated Go/TS
projections, and the generated agent definitions' frontmatter. Lanes declare
capability classes; the loaded policy resolves classes to models. The
adapter's spawn `--model` comes from the resolved policy's preferred model,
so the executing model is still named explicitly before the host runs —
CD-0017 D2's invisible-inheritance concern is answered by the explicit flag
plus the unchanged readback enforcement, not by registry pins.

### D4. CD-0017 clauses amend as follows

- D2's "every registered lane definition must pin a model" and its Invariant 5
  form become: every lane declares a capability class; model resolution is
  owned by the validated host routing policy.
- D3/D8/D9's "legal resolution set is declared in the Concord-owned
  routing-policy record in `contracts/`" becomes: the legal resolution set is
  declared in the loaded routing policy — host state or the repo default —
  digest-bound at load and per attempt.
- CD-0043 D1's "which model runs it" moves to the host side of the
  contract/methodology line: the lane contract declares which capability
  class may run it; the concrete set is host-declared and validated.

### D5. The repo record is a template, not an installation's law

`contracts/routing-policy.v1.json` ships as the default template and keeps
its digest regime for the default path. The repo tree carries zero model
identifiers outside the template, eval fixtures, and test fixtures.

## Consequences

- Fresh installs configure the models they actually have; the template boots
  them, and a host policy replaces it wholesale.
- Per-host ordering stays where it already lives: the operator's
  model-routing plugin config.
- `#251` folds into D2 (registration-time existence checking); its structural
  CI check remains useful for the template itself.
- `#252` stands unchanged: the schema SQL literals describe the default
  template's digest and keep their existing test contract.
- Eval pinning (`check-lane-evals.py`) reads preferred models from the
  template by capability class.

## Rejected alternatives

**Keep repo authority, add host overrides.** Rejected: two authorities for
one bound invites the silent-substitution ambiguity the set exists to
prevent, and the repo still names one operator's credentials.

**Drop the resolution set entirely, trust readback.** Rejected: readback
without a declared bound cannot distinguish a legal fallback from a silent
substitution — it audits what ran, not what was allowed.

**Personal access token (PAT) authenticated bot keeping branches current.** Unrelated surface; see
#289/#297.

## Verification

- `internal/store.TestRoutingPolicyHostOverrideBindsDigestAndDispatchValidation`
  — a host policy changes the bound digest and governs dispatch validation.
- `internal/store.TestRoutingPolicySetButMissingPathFailsTyped` — fail-closed
  on unreadable host state.
- `internal/store.TestRoutingPolicyMalformedAndIncompleteHostPoliciesNameFailures`
  — strict shape and completeness failures name the field or class.
- `internal/store.TestRoutingPolicyRegistryIsDigestPinnedAndMatchesLanePreferences`
  — the default path still reproduces the generated digest.
- Adapter tests cover env-over-default precedence, `--model` from policy,
  `opencode models` existence failures, and the policy-internal cross-anchor.
- `scripts/check-agent-contracts.py` and `scripts/check-lane-evals.py` pass
  with regenerated outputs.
