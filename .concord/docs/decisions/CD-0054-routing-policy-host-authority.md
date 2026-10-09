# CD-0054: The lane registry is model-neutral

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** Lane-registry model neutrality; issue #291
- **Approval:** Operator accepted the drafted decision as written on 2026-08-21; the
  public record is
  [issue #291 comment](https://github.com/Sharper-Flow/concord/issues/291#issuecomment-5375408050)
- **Related:** CD-0017 (worker lanes and readback evidence), CD-0043 (host methodology),
  CD-0034 (host-provenance regime), CD-0058 (no model routing),
  CD-0044 (evidence boundary), CD-0049 (project-local lane delivery)
- **Preserves:** the worker authority boundary and actual executing-model readback

## Context

Models are per-host configuration. A public lane registry cannot declare one
installation's model access as Product truth. Lane intent belongs in the
registry; concrete model selection belongs to the host. Actual executing-model
evidence remains the readback identity under CD-0017 D5 and CD-0058 D2.

## Decision

### D3. The lane registry is model-neutral

The lane registry and its generated schemas and definitions contain no model
pin. A lane declares its capability class. That class describes lane intent,
not a runtime model choice. Host configuration owns concrete model selection.

## Consequences

- Installs configure their accessible models in host configuration, not the
  Product's lane registry.
- Generated lane definitions carry capability classes without a model pin.
- Readback proves the executing identity, not a declared model choice.

## Verification

- `internal/store.TestLaneRegistryIsGeneratedClosedAndDigestPinned` proves the
  capability class is required and the closed registry binds its digest.
- `python3 scripts/generate-agent-lanes.py --check` validates the closed manifest
  schema and verifies its generated model-neutral projections and definitions.
- `python3 scripts/check-agent-contracts.py` checks the generated lane contracts.
