# Concord Agent Tool Surface Identity and Change (TS8)

> **Status:** Accepted and binding until superseded.
> **Decision:** TS8, amended by CD-0042.
> **Current boundary:** Concord is pre-go-live. The current agent surface has one
> generated manifest and one identity: its exact manifest digest.

## Context

The agent surface needs one identity and one change law. The binding inputs
are the accepted TS1 through TS7 contracts and CD-0042's amendment making the
generated current manifest the only pre-go-live surface identity. Concord is
pre-go-live: the current surface has one generated manifest whose exact
digest is its identity.
## Contract

The binding contract is sections 1 through 3: the canonical manifest as the
only source of agent-visible surface, the change rule before go-live with its
four required evidence elements and the named domain-overlap proof, and
digest failure with static loading. Section 4 records the first-go-live
trigger that must define the future compatibility law; section 5 records
evidence and falsifiers, and carries no obligation.
## 1. Current contract

The canonical manifest is the only source for agent-visible tools, operations,
strict schemas, capabilities, consequences, bounds, and generation. It generates
the Go and TypeScript contracts, JSON schemas, fixtures, tests, and documentation
fragments. Generated outputs are checked for drift; hand-maintained copies are not
authoritative.

Every grant, invocation, session-boot packet, and result envelope binds the exact
manifest digest. A digest mismatch fails before a domain effect. There is no surface
version, version range, envelope-version negotiation, down-conversion, alias, or
fallback meaning. Schema, event, workflow, session, and release versions remain
where they identify real current formats or builds; they are not agent-surface
compatibility identities.

The surface is statically registered in the OpenCode adapter. Agents do not search
for or progressively load Concord tools. The current tool and operation names are
those generated from the manifest, including
`concord_work_relate.resolve_overlap`. The adapter remains a thin transport and
permission bridge; the Go core owns authority, transactions, and consequences.

## 2. Change rule before go-live

The primary path changes directly. When the accepted surface changes, remove
replaced fields, negotiation branches, aliases, migration hints, obsolete replay
paths, and prose that existed only for the old development shape. Keep historical
decision and research records readable, but do not keep runtime compatibility code
for hypothetical pre-go-live clients.

Each surface change must provide:

1. a named deterministic PM1/TS1 scenario or executable reproduction showing the
   problem and the passing outcome;
2. the canonical manifest and generated artifacts updated together;
3. strict schema, generated-drift, negative-input, authority, transaction, and
   conformance evidence; and
4. explicit operator acceptance when the change alters Product authority or
   consequence.

The named domain-overlap proof is
`AJ5-resolve-domain-overlap`, bound to
`TestAgentJobsCorpus/AJ5-resolve-domain-overlap`. It proves that an ordinary
relation does not grant overlap authority and that `resolve_overlap` consumes an
operator approval pinned to both work and workflow-contract versions.

## 3. Digest failure and static loading

The adapter sends the digest from its generated manifest. Core accepts only an
exact match with its generated current manifest. Unknown or mismatched digests
produce a typed fail-closed result before any domain call. The adapter reports a
transport/bootstrap failure only when no valid core envelope exists; it never
guesses a contract, accepts unknown variants, or claims a domain effect.

OpenCode loads the generated static exports when a session starts. A changed
manifest therefore requires regenerating the adapter and starting a session with
the matching digest. This is a build/session fact, not a compatibility window.

## 4. First-go-live trigger

Concord remains pre-go-live until an accepted operator decision names the first
supported release, client and model populations, and compatibility cohort. A tag,
installer, or development release does not create that obligation.

That first go-live decision must also define the future compatibility law:

- the contract identity exposed as a supported promise;
- whether semantic version labels add information beyond the manifest digest;
- supported populations and support periods, if any;
- migration and removal rules, if any; and
- evidence required to add, change, or remove a supported operation.

Until that decision, no implementation or living contract may prebuild those
policies.

## 5. Evidence and amendment

TS8 depends on TS1–TS7's deterministic jobs, strict schemas, authority boundaries,
transaction proofs, and envelope rules. TS9 defines the pre-go-live evidence
contract; it does not add a model-trial release gate. Operator acceptance remains
the authority for Product-consequence changes.

Reopen TS8 only when one generated manifest cannot express the current surface
without lossy exceptions, a concrete accepted client requires a different identity,
or static loading cannot safely support a current operation. Any amendment keeps
one generated source, exact digest binding, strict unknown-input rejection, and
fail-closed authority.

## Acceptance criteria

- Given any grant, invocation, session-boot packet, or result envelope
  When the core checks identity
  Then it binds the exact manifest digest and a mismatch fails before any
  domain effect, with no fallback meaning.

- Given a changed accepted surface
  When it lands
  Then the canonical manifest and every generated artifact update together,
  and hand-maintained copies hold no authority.

- Given an adapter digest that does not match the core's current manifest
  When a call arrives
  Then the core fails closed with the typed `manifest_mismatch` before any
  domain call, and recovery is regeneration and session restart, never
  parsing unknown variants.

- Given a domain-overlap resolution request through an ordinary relation
  When the agent attempts it
  Then the ordinary link writes no overlap authority; only the version-pinned
  `resolve_overlap` with operator approval resolves it.

## Verification

Criteria 1 through 3 are structural surface-identity properties proved by the
generated drift validator `scripts/check-agent-contracts.py` together with
the generated contract tests (`adapter/opencode/generated-contract-tests.ts`).
Criterion 4 is proved by the bound `AJ5-resolve-domain-overlap` scenario of
`scenarios/agent-jobs.v1.json`, executed by `TestAgentJobsCorpus`
(`internal/agent/agent_jobs_corpus_test.go`) — the named proof section 2
records.

- Criterion 1 is proved by the validator's digest-binding checks over the
  generated contracts and the manifest.
- Criterion 2 is proved by the validator's generated-drift checks, which fail
  when the manifest and its artifacts disagree.
- Criterion 3 is proved by `TestEnvelopeRejectsUnknownVariantsAndFields`
  (`internal/agent/envelope_test.go`) and the adapter contract tests' strict
  unknown-input rejection.
- Criterion 4 is proved by the bound `AJ5-resolve-domain-overlap` scenario.
