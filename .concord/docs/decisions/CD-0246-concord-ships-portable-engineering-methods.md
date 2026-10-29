# CD-0246: Concord ships portable engineering methods

- **Status:** Accepted
- **Date:** 2026-10-27
- **Scope:** Which engineering methods Concord may ship, and how a worker and
  another agent receive them
- **Amends:** [CD-0043](CD-0043-host-owned-lane-methodology.md) D1;
  [CD-0063](CD-0063-shipped-operator-conduct-rules.md) D1, the sentence that
  methodology stays unshipped;
  [CD-0245](CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md)
  D1, its second paragraph
- **Preserves:** CD-0043 D2 for host-specific procedure; CD-0245 D2-D4;
  CD-0063 D5
- **Approval:** The operator approved the exact decision text and its
  packaging on 2026-10-27, recorded in
  [Concord issue 1367](https://linear.app/sharper-flow/issue/CON-1367/integrate-six-matt-pocock-skill-methods-into-concord-agents-and).

## Context

CD-0043 D1 kept every lane procedure out of Concord, and CD-0063 D1 kept
methodology unshipped. Both rested on one premise: methodology is host-coupled
by construction, so Concord would own prose it cannot verify.

Some methods are not host-coupled. Building a failing reproduction before
guessing a cause, applying a deletion test to a new abstraction, shaping work
into independently verifiable outcomes, and moving a recurring correction to
the owner that can hold it hold in any Product and name no tool. Without a
shipped copy, each machine's host instructions either carry them or omit them,
and a worker's behavior on the same packet differs by machine.

On 2026-10-27 the operator approved shipping portable engineering methods in
generated worker definitions, with host-specific procedure and model settings
kept by the host. The existing installer already registers Concord's skill
path in the host configuration, so a shipped skill reaches every session on the
machine. Dispatch provenance binds the lane definition by content hash but does
not record a skill a worker loads during an attempt.

## Decision

### D1. Concord ships methods that hold in any Product

Concord may ship an engineering method for diagnosing a defect, designing an
interface, shaping work, or improving instructions and checks. A shipped method
names no host tool, machine, model, or private source. Procedure that depends
on a host's tools, models, or configuration stays host-owned and reaches a
worker under CD-0043 D2.

### D2. A worker receives its method in its generated lane definition

A lane's generated definition carries the shipped method that lane's purpose
uses. Dispatch binds that definition by content hash. A lane obligation never
depends on a worker loading a skill, because dispatch does not record a skill
loaded during an attempt.

### D3. Other agents receive methods as shipped skills

A shipped skill may carry a method under D1. It installs through the CD-0063 D2
route, and its registered path makes it available to every session on the
machine. An occasional method ships as a skill, not as always-on instructions.

### D4. A method is guidance, not a gate

A shipped method adds no workflow step, approval, packet field, capability, or
check. It authorizes no source or live-state change. Accepted law and the
assigned scope outrank it.

## Alternatives considered

- **Keep every method host-owned.** Rejected by the operator: workers on
  different machines would follow different methods for the same packet, and
  the release could not state what a worker was told.
- **Deliver worker methods only as skills.** Rejected. Dispatch does not record
  a skill loaded during an attempt, so an obligation carried that way would be
  invisible to provenance.
- **Add methods to the always-on conduct corpus.** Rejected. Diagnosis, design,
  work shaping, and retrospectives are occasional jobs; always-on text grows
  every session's prompt for work most sessions never do.

## Consequences

- The lane generator owns the method text a lane carries. A method change moves
  the generated definition's content hash and ships with a release.
- CD-0043's rejection of fatter generated lane definitions no longer covers a
  portable method; the generator remains the only author of that text.
- `skills/` admits portable method skills beside skills that operate
  Concord-defined structure.

## Required conformance

- The generated definitions of the lanes whose purpose uses a method carry it,
  and the other lanes do not.
- Each admitted method skill passes the shipped-skill admission, portability,
  and reference checks, and installs through the existing skill path.
- The lane packet schema gains no property.

## Verification

- `python3 scripts/check-knowledge-index.py` checks publication identity and hashes.
- `python3 scripts/check-agent-contracts.py` checks the admitted skill inventory, skill portability, and the absent packet methodology property.
- `python3 scripts/test-generate-agent-lanes.py` checks which generated lanes carry which method.
- `python3 scripts/test-installer.py` checks that the admitted skills install and are discoverable through the registered skill path.
