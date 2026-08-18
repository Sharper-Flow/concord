# CD-0015: Typed law relations and Git-derived workflow checks

**Status:** Accepted.
**Approval date:** 2026-08-11.
**Approval:** Operator approval for GitHub issue #44.
**Amended by:** CD-0041 gives every law one Domain home and composes these
relations with architecture-bound work contracts and overlap checks.

## Decision

Concord's durable law authority remains the Git knowledge manifest. SQLite may
hold only a derived projection of that manifest; it cannot author law, relation
edges, or law events.

The manifest evolves compatibly from schema version `1.0` to `1.1`. Version
`1.0` remains readable. Decision and spec records may optionally declare the
closed relations `supersedes`, `refines`, `subordinate_to`, and
`conflicts_with`, each with a target law ID. Relations are authored only by an
operator-approved manifest/spec/decision delta. No per-rule obligation field is
introduced: RFC 2119 prose remains interpretive until a named enforcement path
needs distinct behavior.

`conflicts_with` is an unresolved declaration. It grants no precedence and is
not an amendment. Manifest parsing validates same-manifest decision/spec
endpoints, closed kinds, no self or duplicate edges, acyclic directed graphs,
and exact agreement between supersession edges and `successor` declarations.

The derived projection contains `law_subjects` and `law_relations` only. A
knowledge-index rebuild replaces those rows transactionally for one Git home;
invalid input or rollback preserves the prior projection byte-for-byte.

Workflow contracts reuse `spec_mandate` for referenced law IDs and add bounded
`law_modifies`. The latter must be a subset of the mandate and explicitly
enters the operator-approved amendment path. At contract approval/planning,
all mandated IDs must be currently accepted in the resolved Git home and an
explicit conflict blocks unless a conflicting endpoint is in `law_modifies`.
Before completion, the same bounded check runs without that exception. An
amendment intent therefore permits planning only; completion requires a Git
manifest delta that removes or resolves the conflict.

Heuristics may suggest conflicts in memory or UI, but they never persist rows,
events, or blocking decisions. Concord adds no runtime policy engine, LegalRuleML
dependency, law-authoring domain event, or agent tool.

## Consequences

- Git evidence and the manifest remain the sole law authority.
- Existing `1.0` manifests and workflow contracts remain readable.
- Unknown or stale mandated laws fail closed at consequential workflow
  boundaries.
- Relation changes are auditable through the Git manifest and its blob proofs.

## Implementation evidence

- [`concord-knowledge-index.md`](../concord-knowledge-index.md)
- [`specs-as-laws.md`](../specs-as-laws.md)
- [`workflow-engine-contract.md`](../workflow-engine-contract.md)
- GitHub issue [#44](https://github.com/Sharper-Flow/concord/issues/44)
