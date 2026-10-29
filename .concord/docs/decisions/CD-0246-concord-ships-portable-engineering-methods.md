# CD-0246: Concord ships portable engineering methods

- **Status:** Accepted
- **Date:** 2026-10-27
- **Scope:** Admission and delivery of portable engineering methods
- **Related:** [CD-0043](CD-0043-host-owned-lane-methodology.md), [CD-0063](CD-0063-shipped-operator-conduct-rules.md), [CD-0245](CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md)
- **Approval:** [Concord issue 1367](https://linear.app/sharper-flow/issue/CON-1367/integrate-six-matt-pocock-skill-methods-into-concord-agents-and) records the operator's portable-method approval.

## Context

Some engineering methods hold across Products without depending on a host's
tools, models or configuration. A shipped skill provides one maintained copy
without adding it to every session's always-loaded instructions.

## Decision

### D1. Concord ships methods that hold in any Product

Concord may ship methods for diagnosis, interface design, work shaping and
improving instructions and checks. They name no host tool, machine, model or
private source. Host-specific procedure stays host-owned.

### D3. Agents receive methods as shipped skills

A shipped skill may carry a D1 method. It installs through CD-0063 D2; the
registered path makes it available to host sessions and native subagents.
An occasional method ships as a skill, not always-on instructions. No generated
lane definition or dispatch-provenance record is required.

### D4. A method is guidance, not a gate

A method adds no workflow step, approval, capability or check. It authorizes
no source or live-state change. Accepted law and assigned scope outrank it.

## Alternatives considered

- Keep every method host-owned. Rejected: portable methods would need a
  separately maintained copy on each host.
- Add occasional methods to always-on conduct. Rejected: unrelated sessions
  would pay the context cost.

## Consequences

Skills admit portable methods beside methods operating Concord-defined
structure. The release owns their content; the host owns native execution.

## Required conformance

Each admitted method skill passes admission, portability and reference checks
and installs through the existing skill path.

## Verification

- `python3 scripts/check-knowledge-index.py` checks identity and hashes.
- `python3 scripts/check-doc-contract.py` checks the outline.
- `python3 scripts/check-agent-contracts.py` checks admitted skill inventory and portability.
- `python3 scripts/test-installer.py` checks skill installation and discovery.
