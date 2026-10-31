# CD-0058: Concord does not route models

- **Status:** Accepted
- **Date:** 2026-08-22
- **Scope:** Concrete model selection
- **Related:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D6

## Context

Available models and their configuration belong to the host. A second routing
owner inside Concord would duplicate that configuration.

## Decision

The host selects concrete models for its agents and native Tasks. Concord has
no routing policy, model pin, model fallback or subagent model-readback gate.
Recorded workflow results do not attest which model produced them.

## Alternatives considered

- Route models in Concord. Rejected: it creates a second owner for host configuration.

## Consequences

Model configuration remains host-owned. Independent review and native receipt
requirements remain under CD-0242 D5 and D8.

## Verification

- `python3 scripts/check-knowledge-index.py` checks identity and hashes.
