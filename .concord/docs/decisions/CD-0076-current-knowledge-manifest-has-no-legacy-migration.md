# CD-0076: The current knowledge manifest has no legacy migration path

- **Status:** Accepted
- **Date:** 2026-08-25
- **Scope:** Concord knowledge manifest schema 1.2; `MigrateLegacyKnowledgeManifest`;
  CD-0222 D9.2; CD-0062 D3; issue #490
- **Approval:** Operator approved the subtractive resolution on 2026-08-25 and
  stated that pre-launch repository history creates no runtime compatibility
  obligation.
- **Related:** CD-0222 D9.2, CD-0042 D1, CD-0062 D1-D3
- **Preserves:** CD-0222 D2-D3 and CD-0062 D1-D2/D4-D5

## Context

A manifest migration that no command, agent operation, or store-open path
calls supplies no compatibility guarantee. Maintaining it adds a second
interpretation contract that no Product path performs.

## Decision

### D1. Schema 1.2 is the only Concord knowledge manifest schema

The Go parser, the Python validator, the generator, and the JSON Schema accept
only knowledge manifest schema 1.2. Its Domain registry and Domain-scoped record
shape are required.

This rule applies only to the knowledge manifest shards under `.concord/docs/knowledge/`,
composed by `scripts/knowledge_index.py`, and their contract
`contracts/concord-knowledge-index.v1.schema.json`.
It does not change schema version 1.0 on the Domain registry or any unrelated
repository surface.

### D2. The legacy migration contract ends before launch

Concord does not add a flag, compatibility
shim, startup migration, or operator command for schema 1.0 or 1.1 manifests.


### D3. Root-home rationale validation remains current law

CD-0062 D1 and D2 remain unchanged. Root-homed law must state a bounded
`product_wide_rationale`, and child-homed law must not state one. The rationale
continues through the SQLite projection for PM1.Q10 as CD-0062 D4 requires.

## Consequences

- The current manifest has one accepted shape and one validation path.
- Schema 1.0 and 1.1 knowledge manifests fail before record processing.
- Advance knowledge import remains outside this decision. Issue #295 owns that
  work and cannot implicitly add another manifest interpretation path.
- Historical decision records keep their original text. This record states the
  current amendment without rewriting earlier decisions into false history.

## Rejected alternatives

**Wire the migration during store open.** Rejected because the Product has no
released compatibility cohort and no upcast exists ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

**Expose an operator migration command.** Rejected for the same reason. A new
surface would make obsolete repository history into a supported runtime case.

**Keep schema 1.0 and 1.1 parsing behind a flag.** Rejected because a disabled
compatibility path still retains the second contract and its maintenance cost.

**Rewrite CD-0222 or CD-0062.** Rejected because those records accurately state
the decisions accepted at their dates. This amendment owns the current rule.
