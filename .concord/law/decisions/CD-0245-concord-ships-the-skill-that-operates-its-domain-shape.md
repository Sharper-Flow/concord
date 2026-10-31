# CD-0245: Concord ships release-versioned portable skills

- **Status:** Accepted
- **Date:** 2026-10-23
- **Scope:** Which skills Concord may ship; the `skills/` boundary
- **Amends:** [CD-0043](CD-0043-host-owned-lane-methodology.md) D3; the `skills/` sentence of [CD-0063](CD-0063-shipped-operator-conduct-rules.md) D2
- **Preserves:** CD-0043 D1 as amended by CD-0246, D2, and D4: host-specific lane procedure stays host-owned
- **Approval:** The operator approved the exact amendment wording on 2026-10-23 and the publication-only heading and check section on 2026-10-26, recorded in [Concord issue 1318](https://linear.app/sharper-flow/issue/CON-1318/permit-concord-to-ship-domain-shape-stewardship-through-its-existing).

## Context

CD-0043 D3 keeps `skills/` empty. It rejected Concord-shipped skills because
no consumer needed one and because skills would need their own versioning
regime. CD-0063 D2 built the install route: skills install once under the
versioned data root, and a project holds a pointer.

Operating Concord's law structure needs judgement that a validator cannot
supply. Shipped skills carry that occasional method through the existing
release and installation route.

## Decision

### D1. Concord ships skills that operate Concord-defined structure

Concord may ship a skill when the skill operates a structure that accepted
Concord law defines, and Concord's checks verify the result.

[CD-0246](CD-0246-concord-ships-portable-engineering-methods.md) D3 also
admits a skill that carries a portable engineering method. Host agents and
native subagents use the shipped skill path; host-specific procedure stays
host-owned under CD-0043 D1.

### D2. A shipped skill is versioned by the release

A shipped skill lives in `skills/`, installs under the versioned data root
through the CD-0063 D2 route, and changes only with a release. The release
version identifies its content. No separate skill-versioning regime exists.

### D3. A shipped skill names no host and no tool it does not ship

A shipped skill meets the CD-0063 D3 portability rule. It names no machine,
private repository, or host tool. It calls only Concord operations and the
commands the Product itself declares in `.concord/tooling.v1.json`.

### D4. Tools check; the skill judges

A shipped skill never substitutes for a check. Each step whose result a tool
can verify names the Concord check or read that verifies it. The skill does
not record evidence, approval, or law on its own authority.

## Alternatives considered

- **Keep admitted skills host-owned.** Rejected: other machines and
  Products would not receive them, and they could drift from Concord's checks.
- **Ship them as always-on instructions.** Rejected. These methods are
  occasional jobs. Always-on text grows every session's prompt, against goal D.

## Consequences

- `skills/README.md` records the admission rule in D1.
- A skill change ships on the release cadence.
- CD-0043's verification line "`skills/` contains only `README.md`" is
  replaced by the checks below.

## Required conformance

- A repository check refuses a `skills/` entry that is not listed as admitted
  under D1.
- A content check refuses a shipped skill that names a host path, machine, or
  tool outside the release and the Product's declared tooling.
- An install test proves the skill installs under the versioned data root and
  is discoverable through the registered skills path.

## Verification

- `python3 scripts/check-knowledge-index.py` checks publication identity and hashes.
