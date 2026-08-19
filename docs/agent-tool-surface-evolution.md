# Concord Agent Tool Surface Identity and Change (TS8)

> **Status:** Accepted and binding until superseded.
> **Decision:** TS8, amended by CD-0042.
> **Current boundary:** Concord is pre-go-live. The current agent surface has one
> generated manifest and one identity: its exact manifest digest.

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
