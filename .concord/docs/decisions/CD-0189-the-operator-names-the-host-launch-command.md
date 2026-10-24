# CD-0189: Session context probes the calling host once

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** session-prepare's registry probe of the calling host
- **Related:** CD-0182, CD-0049, CD-0078, CD-0088, CD-0093
- **Approval:** The operator approved reopening CD-0093 D4 on 2026-09-28
  after evidence that launcher-started sessions skip the host wrapper. The
  pull request is the public record.
- **Approval:** Concord (CON) issue CON-910 (2026-10-13), under the Snowball coordinator's operator-delegated approval.
- **Preserves:** CD-0093 D2's one-directory and one-registry binding, CD-0093
  D3's fail-closed resolution, CD-0049 D4's refusal of a degraded start,
  CD-0078's host ownership of placement

## Context

An operator can run the host through a wrapper program. A host-started session enters
managed work through `concord_work_start`, which launches nothing (CD-0098).
session-prepare must verify the registry that the calling host resolves. It probes the
calling host once, under the calling host's inherited environment, in the
claimed worktree.

## Decision

### D1. Retired: the configured launch command

The host owns session placement. Concord starts no host process.

### D2. The registry session-prepare verifies is the one the calling host resolves

session-prepare runs one bare registry probe in the claimed worktree under
the calling host's inherited environment. That environment resolves the
registry the calling session runs with, so the verification constrains the
host that executes. The probe is bounded: one probe, one document, no second
probe through any configured command, because no configured command exists.

### D3. Retired: the argv value and its launch refusals

Probe and registry refusals stay
typed: an unreadable registry document refuses deterministically before
managed action.

### D4. Retired: the launch surfaces

session-prepare launches nothing:
`concord_work_start` moves the calling session, which keeps running in the
host process that called it. Adapter worker dispatch and the move-session
version diagnostic stay on the bare host. Worker lanes are not
operator-resumable sessions.

### D5. The law moves with the code

`OPENCODE_BIN` stays a test seam and acquires no operator meaning. Concord
ships no wrapper name and hard-codes none.

## Costs

- session-prepare's probe cost is unchanged: one bare probe per prepared
  session, in the claimed worktree.

## Alternatives considered

- **Keep the configured command for session-prepare's probe.** Rejected:
  session-prepare launches nothing, so a configured launch command would
  verify a host that does not execute.
- **Read the host config file directly and skip the probe.** Rejected:
  config merging is host logic, and the registry probe already relies on the
  host's merged output.
- **An environment variable.** Rejected: a long-lived shell never sees a
  later change, and it is invisible in host config.
- **Keep a launch-only option.** Rejected: it leaves Concord owning a launch
  selection the host owns.

## Consequences

Sessions are started by the host, under the host's own wrapper and
environment. Concord's identity steps verify the registry that the calling
session actually resolves, so a wrapper cannot run a different agent
registry than the one the session asserted against.

## Verification

- `go test ./cmd/concord/ -run TestSessionPrepareProbesTheCallingHostOnce`
  proves D2: one bare probe on the session-prepare side.
- `go test ./cmd/concord/ -run TestDeterministicHostRegistryRefusalsExit2`
  proves D3: probe and registry refusals are typed and deterministic.
- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-knowledge-index.py`,
  `python3 scripts/generate-knowledge-index.py --check`,
  `python3 scripts/check-knowledge-closure.py`, and
  `python3 scripts/check-cd-allocation.py --no-fetch` pass.
