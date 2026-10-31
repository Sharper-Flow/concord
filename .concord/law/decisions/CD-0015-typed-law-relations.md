---
{
  "id": "CD-0015",
  "title": "Typed law relations and Git-derived workflow checks",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": [
    "durable-authority",
    "workflow-engine"
  ]
}
---
# CD-0015: Typed law relations and Git-derived workflow checks

**Status:** Accepted.
**Approval date:** 2026-08-11.
**Approval:** Operator approval for GitHub issue #44.

## Decision

CD-0222 gives every law one Domain home and composes these relations with
architecture-bound work contracts and overlap checks. Planning admits an explicit
conflict only when `law_modifies` names the endpoint whose record declares it,
because only that record can withdraw the declaration.
The store keeps current rows, and no domain event exists
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
Operator consent follows [CD-0243 D7](CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
throughout this record, including tables and acceptance criteria.
Other admission, safety, evidence, and historical-pin requirements remain required.

Concord's durable law authority is the default-branch law folder under
`.concord/law/`
([law-folder-authority](../constitution/law-folder-authority.md)). Relations
are in-file metadata of the law files; SQLite holds no law projection and
cannot author law, relation edges, or law events.

Decision and spec records may optionally declare the
closed relations `supersedes`, `refines`, `subordinate_to`, and
`conflicts_with`, each with a target law ID. Relations are authored only by an
operator-approved delta to the owning law files. No per-rule obligation field is
introduced: RFC 2119 prose remains interpretive until a named enforcement path
needs distinct behavior.

`conflicts_with` is an unresolved declaration. It grants no precedence and is
not an amendment. Law-metadata parsing validates decision/spec
endpoints, closed kinds, no self or duplicate edges, acyclic directed graphs,
and exact agreement between supersession edges and `successor` declarations.

Workflow contracts reuse `spec_mandate` for referenced law IDs and add bounded
`law_modifies`. The latter must be a subset of the mandate and explicitly
enters the operator-approved amendment path. At contract approval/planning,
all mandated IDs must be currently accepted in the resolved Git home and an
explicit conflict blocks unless the endpoint whose record declares it is in
`law_modifies`.
Before completion, the same bounded check runs without that exception. An
amendment intent therefore permits planning only; completion requires a Git
law delta that removes or resolves the conflict.

Mandated bare law IDs resolve in the Product's designated knowledge home.
A source-qualified `<project_id>/<law_id>` reference resolves only
through that Project's canonical knowledge locator.

Heuristics may suggest conflicts in memory or UI, but they never persist rows
or blocking decisions. Concord adds no runtime policy engine, LegalRuleML
dependency, law-authoring store write, or agent tool.

## Consequences

- Git evidence and the law folder remain the sole law authority.
- Existing workflow contracts remain readable.
- Unknown or stale mandated laws fail closed at consequential workflow
  boundaries.
- Relation changes are auditable through Git and its blob proofs.

## Implementation evidence

- [`specs-as-laws.md`](../../docs/specs-as-laws.md)
- [`workflow-engine-contract.md`](../specs/workflow-engine-contract.md)
- GitHub issue [#44](https://github.com/Sharper-Flow/concord/issues/44)
