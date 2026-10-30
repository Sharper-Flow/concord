# CD-0220: Repository authoring follows approved scope

- **Status:** Accepted
- **Date:** 2026-10-10
- **Scope:** Authored and generated repository files, including `.concord/`
- **Related:** [CD-0006](CD-0006-concord-root-product-policy.md), [CD-0036](CD-0036-breaking-law-cutovers.md), [CD-0072](CD-0072-unguarded-trunk-file-writes.md), [CD-0159](CD-0159-authority-tier-for-product-law.md), [development authority](../managed-development-authority.md)

## Context

The `.concord/` directory holds law, knowledge metadata, configuration and
generated projections. A path alone determines neither edit authority nor law
status. Permission to write a file is not approval of its meaning.

## Decision

### D1. The coordinator owns scope and approval routing

The coordinator may draft law during shaping and integrate authorized changes.
An execution-time law conflict returns to the existing planning or
contract-revision boundary before affected execution. CD-0006, CD-0159 and
CD-0036 retain approval, authority-tier, revision and cutover ownership.
A draft, commit, content hash or subagent report does not enact law.

### D2. Authors remain inside approved scope

An assignment authorizes only its approved source changes. A law addition or
amendment requires the contract's added or modified binding for that law;
mandated or obligation bindings alone grant no edit authority. The coordinator
resolves missing scope, conflicts and integration order. Native Task does not
expand approved Product, Project, Domain, outcome or law authority.

### D4. Generated artifacts retain their owner

Change a generator's authorized inputs and regenerate its projections; never
hand-edit generated files. Changing a source, record status or authority tier
is source authoring. Refreshing a hash or canonical encoding
approves neither content nor evidence.

### D5. Host permission and review remain separate

Host permissions govern native tools and Task execution. Concord adds no
subagent tool-capability interceptor, sandbox or producer-identity attestation.
Independent review examines the actual Git delta, approved scope and law-change
authority. Structural checks validate artifacts, not who wrote or approved them.
CD-0072's host file-write boundary remains unchanged.

### D6. Outside-workflow work retains its authority boundary

An authorized host actor may work outside the workflow under host permissions
and repository rules. Applicable Product-law approval and independent review
remain required. This creates no workflow verdict or completion evidence.

## Alternatives considered

- Require the coordinator to author every file. Rejected: integration ownership
  does not require one author.
- Treat a generated hash as approval. Rejected: it establishes byte identity only.

## Consequences

Native subagents can deliver authorized source changes without Concord launcher
machinery. Scope, law-change approval and independent review retain their owners.

## Verification

- `python3 scripts/check-knowledge-index.py` checks identity and content hashes.
The passing structural checks do not prove scope or approval. Independent
review must compare the actual delta with the approved scope and evidence.
