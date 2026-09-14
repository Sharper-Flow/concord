# CD-0134: The agent surface splits by authority boundary

- **Status:** Accepted
- **Date:** 2026-09-12
- **Scope:** Agent-surface Domain granularity, TS1–TS9 law homing, and the registry migration
- **Approval:** The operator approved this bounded decision objective in Concord work-2e8afff1f907a403561806e5; this record states the resulting decision.
- **Related:** CD-0005, CD-0041 D2–D4 and D9, CD-0060 D1 and D4–D6, CD-0017, CD-0082, and TS1–TS9
- **Amends:** Nothing. The later registry delta enacts this decision without changing the accepted TS contracts.
- **Preserves:** One-home law, non-empty Domains, Git registry authority, the static generated agent surface, and demand-driven migration

## Context

The registry has one `agent-surface` Domain. It owns job and tool contracts, call context, transport, results, evolution, evidence, and worker lanes.

The TS1–TS9 records describe distinct contract boundaries. CD-0017 also separates the worker contract from the host-owned worker process.

The current Domain is valid, but it is too broad for precise law and work placement. A split must improve authority and navigation without turning each document into a Domain.

TS2 already names the material boundaries for tool granularity. Those boundaries also provide evidence for Domain decomposition when they describe stable ownership, invariants, and architectural contracts.

## Decision

### D1. A Domain earns a split by a proved boundary

A proposed child Domain earns a registry entry only when all of these conditions hold:

1. It owns a stable purpose, behavior, invariant set, and architectural contract.
2. It has a material boundary in authority, transaction, consequence, approval, retry, recovery, output, or caller intent.
3. At least one current law record homes there at enactment. An empty child is not permitted.
4. The boundary gives an agent a distinct and useful law or work browse path.
5. Every affected law and work footprint has one explicit destination in the migration.

A document title, repository path, package, shared entity, or implementation language does not earn a split. A specification boundary is evidence, not an automatic Domain boundary.

The rule rejects a split when the proposed child has no current law, no distinct authority or recovery behavior, or no useful browse distinction. It also rejects a split that creates duplicate homes or an inferred compatibility layer.

### D2. The agent-surface Domain becomes a parent with five children

`agent-surface` remains a current parent Domain for cross-surface architecture. Its purpose narrows to the composition and concordance of the child boundaries.

The registry adds these current children under `agent-surface`:

| Domain | Boundary | Primary law records |
|---|---|---|
| `agent-contract` | Agent jobs, granularity, read intents, mutation intents, and operation contracts | TS1, TS2, TS3, TS4, CD-0025, CD-0035, CD-0038, CD-0069, CD-0113 |
| `agent-call-boundary` | Scope, authorization, idempotency, result, error, evidence, and pagination semantics | TS5, TS7, CD-0023, CD-0027, CD-0037, CD-0071, CD-0080, CD-0132 |
| `agent-adapter` | OpenCode registration, host context, permission bridge, and CLI transport | TS6, CD-0084, CD-0085, CD-0087, CD-0088, CD-0090 |
| `worker-lanes` | Typed lane contracts, dispatch identity, worker evidence, lane visibility, and host-owned worker execution | CD-0017, CD-0034, CD-0043, CD-0044, CD-0054, CD-0056, CD-0058, CD-0063, CD-0064, CD-0067, CD-0070, CD-0081 |
| `agent-evolution` | Manifest identity, pre-go-live surface change, and deterministic change evidence | TS8, TS9, CD-0024, CD-0042 |

The parent retains CD-0005 because that record indexes the complete agent surface and its cross-child invariants. CD-0134 also remains at the parent because it governs the decomposition itself. The migration marks the child-specific applicability of these clauses without creating a second home.

The table assigns all forty pre-existing law records homed to `agent-surface`. A record keeps one home even when its contract applies to more than one child. Historical clauses remain readable and keep their existing law identities.

### D3. The split does not split the callable surface

The five children are architectural and browse boundaries. They do not create one tool set per child, one adapter per child, or one manifest per child.

The current generated manifest remains one static agent surface. The eight domain tools and later coordinator-only additions remain governed by TS1–TS9 and CD-0042.

Cross-child work names the parent and affected children in its architecture binding. A leaf-specific change names only the child whose law or assumptions it can affect.

### D4. Architecture relations follow the child boundary

Each child receives explicit `depends_on` relations only where its behavior uses another Domain. Each relation names at least one current governing law.

The parent retains the existing durable-authority and workflow-engine relations for cross-surface composition. Child relations do not arise by inheritance. The migration declares them explicitly and removes no relation without a replacement.

### D5. The migration is one registry and law-home delta

The migration performs these ordered changes in one accepted Git change:

1. Add the five child Domains, narrow the parent purpose, and declare non-empty law ownership.
2. Re-home the forty records in D2 and add bounded applicability for cross-child records.
3. Rebind each active Product-changing work footprint to the parent or its affected children.
4. Replace child-specific architecture relation tuples and retain governing law IDs.
5. Compose the knowledge index, recompute the registry and manifest digests, and regenerate every dependent artifact.
6. Rebuild the SQLite Domain projection from the new Git authority before child-scoped reads become current.

The migration does not add aliases, fallback homes, or old-registry compatibility. A changed generated digest fails closed under TS8 until the adapter and core use the matching artifacts.

CD-0082 makes later migration demand-driven. This decision requires the registry delta when the split is enacted, but it does not require a Product-wide migration program.

### D6. No runtime split occurs in this change

This record decides the target architecture only. It does not edit `docs/knowledge/domain-registry.json`, change law homes, update work bindings, or rebuild the SQLite projection.

The registry and projection migration require a separate implementation change with the deterministic checks named in TS8 and TS9.

## Consequences

- Law and work reads can distinguish contract, call, adapter, worker, and evolution concerns.
- Cross-surface architecture remains visible through the parent Domain.
- The migration changes registry and generated digests, so matching artifacts must move together.
- The callable agent surface remains one generated, digest-bound contract.
- Future Product-changing work must name the parent or the child boundaries it affects.

## Verification

- `scripts/check-doc-contract.py` validates the decision outline, sentence bounds, and banned-phrase rules.
- `scripts/check-knowledge-index.py` validates the record shard, law home, and relation shape.
- `scripts/check-law-coverage.py` validates that the decision has a coverage shard.
- `scripts/check-json.py` runs the repository JSON and generated-contract validators.

The checks validate this decision record and its current registry state. They do not claim that the split migration has happened.
