# CD-0086: episode is an optional external authority

- **Status:** Accepted
- **Date:** 2026-08-30
- **Scope:** episode ownership, optionality, Product-scoped integration, the
  promotion-receiving seam, C20, and issue #46
- **Approval:** The operator resolved C20 as external and Product-scoped, then
  clarified that Concord must not require episode for all users.
- **Related:** CD-0016, CD-0026, C20, and issue #46
- **Preserves:** Concord authority for Product law and formalized knowledge;
  episode authority for its memory store, recall, and promotion state

## Context

Concord can use episode for durable agent-decision memory. episode is a separate
general-purpose tool with its own storage, recall behavior, and users. Concord
does not need that memory capability to coordinate Product work, enforce law,
run workflows, or maintain its own state.

C20 asked whether episode should stay external and Product-scoped or become
Concord-owned. Issue #46 initially held that answer behind automatic Product
scoping and a real multi-project probe. That probe can test the quality of the
external bridge, but it is not needed to establish whether every Concord user
must install and run episode.

The promotion-receiving contract gives Concord a bounded destination for memory
that an episode operator chooses to formalize. It does not require a live sender
and does not make episode part of Concord correctness.

## Decision

### D1. episode remains an external, optional authority

Concord does not own or absorb episode. episode remains the authority for its
memory store, recall behavior, and promotion state.

Concord remains complete and usable when episode is absent. No Concord build,
installation, startup, storage, authorization, or core workflow may require
episode. An operator can use every Concord capability without configuring it.

### D2. Configured episode integrations are Product-scoped

When an operator configures episode, the integration supplies Product context
to memory operations. episode remains independent and Product-scoped at the
integration boundary. Concord does not copy episode memory into SQLite or make
episode state part of Product authority.

Automatic Product derivation belongs to episode. Its implementation is
follow-up work in the episode project, not a Concord runtime requirement.

### D3. Concord owns only the promotion destination

Concord owns Product law and the formalized `spec` or `decision` target. episode
owns the source memory and its `Promoted { target }` state. The receiving seam
in `vertical-integration.md` defines target identity, provenance, structural
acknowledgement, and recall exclusion.

When episode is absent, no promotion request exists and the seam has no runtime
effect. Concord adds no event, write surface, fallback memory store, or required
validation for that absence.

### D4. The Product-scoping probe is a reopen trigger

Automatic Product scoping and its real multi-project probe do not block this
decision. The probe can reopen ownership only if it shows that an external
episode cannot resolve Product and work identity through Concord orchestration,
or if maintaining the bridge costs more than ownership.

## Consequences

- Operators can adopt Concord without adopting episode.
- Operators who configure episode add Product-scoped agent memory without
  changing Concord authority.
- The episode project owns automatic Product derivation and probe evidence.
- C20 is resolved. C8 remains limited to lgrep and vision.
- No runtime, storage, installer, schema, or command change follows from this
  decision.

## Rejected alternatives

**Require episode for every Concord installation.** Rejected because durable
agent-decision memory is optional. Product coordination and law enforcement do
not depend on it.

**Own or absorb episode now.** Rejected because no measured integration failure
justifies a second memory authority inside Concord.

**Keep C20 open until the probe runs.** Rejected because the probe tests bridge
quality. It does not decide whether episode is optional for Concord users.

**Implement automatic Product derivation in Concord.** Rejected because episode
owns recall context and memory scope. Concord supplies Product context only at
the configured integration boundary.

## Verification

- The current build, installation, startup, storage, authorization, and workflow
  contracts contain no required episode dependency.
- `clarifications.md` records the accepted boundary under R7 and removes C20
  from the open-question set.
- `vertical-integration.md` records optionality, authority, Product scope, the
  inactive-when-absent seam, and the probe reopen trigger.
- The knowledge manifest registers this record as an accepted decision with an
  out-of-scope coverage reason because it changes no runtime behavior.
- Repository document, knowledge-index, and link validators pass.
