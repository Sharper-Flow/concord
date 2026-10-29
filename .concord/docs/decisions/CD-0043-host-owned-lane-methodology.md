# CD-0043: Host-specific methodology stays host-owned

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** Ownership of engineering methodology and shipped skills
- **Related:** [CD-0245](CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md), [CD-0246](CD-0246-concord-ships-portable-engineering-methods.md), [capability placement](../capability-placement.md)

## Context

Host-specific procedure depends on tools, models and configuration that Product
knowledge does not own. Portable methods need no such dependency.

## Decision

### D1. Methodology is not durable coordination state

Procedure that depends on a host's tools, models or configuration stays
host-owned. Portable engineering methods may ship under CD-0246. Neither
procedure nor a loaded skill establishes Product truth or verification evidence.

### D3. Skills operate Concord structure or carry portable methods

CD-0245 admits skills that operate a structure defined by Concord law and
checked by Concord. CD-0246 also admits portable engineering methods.
The existing skill installation path makes them available to native host agents.

### D4. Coverage names the owner

Coverage that defers host-specific methodology names the host as its owner;
the deferral creates no missing Concord runtime capability.

## Alternatives considered

- Store host-specific procedure as Product knowledge. Rejected: it confuses
  host configuration with Product truth.
- Make an occasional method always-loaded conduct. Rejected: it adds context
  to sessions that do not need it.

## Consequences

The host owns its prompt surfaces and native Task methodology. Concord ships
admitted skills without recording or attesting subagent execution.

## Verification

- `python3 scripts/check-agent-contracts.py` checks shipped-skill admission.
- `python3 scripts/check-doc-contract.py` checks the decision outline.
- `python3 scripts/check-knowledge-index.py` checks identity and content hashes.
