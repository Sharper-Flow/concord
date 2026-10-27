---
name: concord-codebase-design
description: Design or restructure a module's interface and decide where a seam belongs. Use when adding or changing a function, type, package, or service boundary, judging whether an abstraction earns its place, or making code testable through its interface.
---

# Codebase design

Use this vocabulary and these tests when code is designed or restructured. They
guide judgement; they are not a target every module must reach, and they grant
no scope beyond the assigned work. Preserve transaction, data-safety, and
ownership boundaries the Product's law already sets.

## Canonical references

- [Shipped method boundary](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Domain ownership at package boundaries](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md):
  in a Concord-managed Product, a seam between packages is also a Domain
  ownership question. Read it with `concord_domain.footprint` before moving one.

## Vocabulary

Use these terms consistently.

- **Module**: anything with an interface and an implementation, at any scale:
  a function, a type, a package, a service.
- **Interface**: everything a caller must know to use the module correctly: its
  types, and also its invariants, ordering constraints, error modes, required
  configuration, and performance characteristics.
- **Depth**: the behavior a caller can use per unit of interface it must learn.
  A deep module hides much behind little; a shallow one asks callers to learn
  nearly as much as it does.
- **Seam**: the place where a module's interface lives and where behavior can
  vary without editing the caller. Where it goes is its own decision.
- **Adapter**: a concrete thing that satisfies an interface at a seam.
- **Leverage** is what callers gain from depth; **locality** is what
  maintainers gain: change, defects, and verification concentrate in one place.

## Tests to apply

- **Deletion test.** Imagine deleting the module. If its complexity vanishes,
  it was a pass-through; remove it rather than add another. If the complexity
  reappears across its callers, it earns its place.
- **One adapter is a hypothetical seam.** Introduce a seam only where something
  varies across it now, such as production and test. A single-adapter seam is
  indirection.
- **The interface is the test surface.** Callers and tests cross the same seam.
  A test that must reach past the interface signals a module of the wrong shape.
- **Shrink the interface.** Ask whether a caller needs fewer operations, simpler
  parameters, or fewer facts to learn, and whether more can be hidden inside.
- **Accept dependencies and return results.** A module that receives its
  dependencies and returns values is testable without reaching inside it.

## Deepening a cluster

Classify what the cluster depends on; the class decides how the deepened module
is tested across its seam.

1. **In-process** computation and memory: merge and test through the new
   interface directly.
2. **Local stand-in** available, such as an in-memory store: test with the
   stand-in; the seam stays internal.
3. **Owned remote service**: put an interface at the seam, with a production
   adapter and an in-memory adapter for tests.
4. **Third-party service**: take it as an injected dependency and supply a test
   double.

Write the new tests at the deepened interface. Old tests of superseded shallow
modules are removed only when the governing retirement rule covers them; the
existence of a new test is not by itself that rule.

## Compare designs

For a consequential interface, sketch at least two materially different
interfaces before choosing. Compare them on depth, locality, seam placement, and
what each forces callers to know. Record the choice and the rejected option in
the work's design record, not in code comments.
