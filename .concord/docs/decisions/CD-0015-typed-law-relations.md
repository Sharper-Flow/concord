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

Amended 2026-10-01 by CD-0200 D5: a relation whose target lives outside the
declaring manifest names the target's source Project in a structured field,
never by packing the qualified form into the target ID. The relation
validates over the Product's verified source set at the declaring source's
rebuild and again at every consequential boundary: an unregistered target
Project refuses, and a cross-source `conflicts_with` pair blocks the
rebuild and every consequential check until an accepted relation or
amendment resolves it. Every cross-source `supersedes` edge refuses fail
closed: supersede within the declaring source, or amend the shared law
through its authoring home. No agreement check admits the edge. A non-home
source may not declare `refines` or `subordinate_to` toward shared-home
law; no precedence between sources is ever inferred. The target law must
resolve. A peer with a usable projection answers from its verified
projected rows; a peer with no usable projection (never rebuilt, cleared,
or stamped incomplete over law Domain rows the shared home's absent
registry forced it to omit) resolves the endpoint from its verified Git
head, where an unreadable head, an absent manifest, or an undeclared target
law refuses the rebuild admission. A source-first rebuild stamps its
watermark incomplete over the omitted Domain rows, and a demand-freshness
rebuild backfills them once the registry exists. Cross-source relations
project no `law_relations` row — the same-home foreign keys cannot
reference another source — and project instead into
`law_cross_source_relations` with their target source; the consequential
boundary, not the rebuild alone, is the enforcement point.

A knowledge-index rebuild replaces the derived law rows transactionally for
one Git home; invalid input or rollback preserves the prior projection
byte-for-byte.

Workflow contracts reuse `spec_mandate` for referenced law IDs and add bounded
`law_modifies`. The latter must be a subset of the mandate and explicitly
enters the operator-approved amendment path. At contract approval/planning,
all mandated IDs must be currently accepted in the resolved Git home and an
explicit conflict blocks unless a conflicting endpoint is in `law_modifies`.
Before completion, the same bounded check runs without that exception. An
amendment intent therefore permits planning only; completion requires a Git
manifest delta that removes or resolves the conflict.

Amended 2026-10-01 by CD-0200 D6: over a registered source set, a mandated
bare law ID resolves across every verified source, refuses as ambiguous when
two sources hold it, and names the resolving source's repository in the law
context. A source-qualified `project_id/law_id` reference resolves only
through that Project's canonical knowledge locator.

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
