# CD-0189: Session context probes the calling host once

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** session-prepare's registry probe of the calling host; the
  retirement of the configured launch command and its Go launch obligations
- **Related:** CD-0182, CD-0049, CD-0078, CD-0088, CD-0093
- **Approval:** The operator approved reopening CD-0093 D4 on 2026-09-28
  after evidence that launcher-started sessions skip the host wrapper. The
  pull request is the public record.
- **Amended:** Concord (CON) issue CON-910 (2026-10-13) removes the configured launch command and
  every Go launch obligation under the Snowball coordinator's
  operator-delegated approval. The calling-host probe obligation survives
  for session-prepare.
- **Preserves:** CD-0093 D2's one-directory and one-registry binding, CD-0093
  D3's fail-closed resolution, CD-0049 D4's refusal of a degraded start,
  CD-0078's host ownership of placement

## Context

An operator could run the host through a wrapper program, and CD-0093 D4
first fixed the launch command to the bare host executable, so sessions the
entry route started skipped that wrapper. Live evidence from 2026-09-28
showed the cost, and this record moved the setting into host configuration
as a launch option beside the session opener.

The launch removal retires the option with it: the entry route, the
opener, and the option are gone together; a host-started session enters
managed work through `concord_work_start`, which launches nothing (CD-0098).
What
survives of this record is the probe discipline: session-prepare probes the
calling host once, under the calling host's inherited environment, in the
claimed worktree.

## Decision

### D1. Retired: the configured launch command

The launch option, its argv form, its validation rules, and every Go-core
launch obligation are removed. Concord reads no launch option from host
configuration, resolves no launch command, and starts no host process.

### D2. The registry session-prepare verifies is the one the calling host resolves

session-prepare runs one bare registry probe in the claimed worktree under
the calling host's inherited environment. That environment resolves the
registry the calling session runs with, so the verification constrains the
host that executes. The probe is bounded: one probe, one document, no second
probe through any configured command, because no configured command exists.

### D3. Retired: the argv value and its launch refusals

The argv value, its malformed-value refusals, and its second-document
mismatch refusals retired with the option. Probe and registry refusals stay
typed: an unreadable registry document refuses deterministically before
managed action.

### D4. Retired: the launch surfaces

The Product/work launch, the Project-path launch, and the session registry
probe of a launch are removed. session-prepare launches nothing:
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
- The launch-time probe costs of the configured command retired with the
  launch.

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
