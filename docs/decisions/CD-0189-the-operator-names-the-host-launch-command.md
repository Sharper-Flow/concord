# CD-0189: The operator names the host launch command

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** Which program `concord session` starts and probes; the
  `host_command` option and every Go-core host invocation it governs
- **Amends:** CD-0093 (D4 and D5), CD-0176 (the D3 restatement)
- **Related:** CD-0182, CD-0049, CD-0078, CD-0088, CD-0093
- **Approval:** The operator approved reopening CD-0093 D4 on 2026-09-28
  after evidence that launcher-started sessions skip the host wrapper. The
  pull request is the public record.
- **Preserves:** CD-0093 D2's one-directory and one-registry binding, CD-0093
  D3's fail-closed resolution, CD-0049 D4's refusal of a degraded start,
  CD-0078's host ownership of placement

## Context

An operator can run OpenCode through a host wrapper: a small program that
sets up the host environment and supervises an in-TUI restart. CD-0093 D4
fixed the host command to the bare `opencode` executable, so every session
the launcher, `concord zl`, or the CD-0182 session opener starts skips that
wrapper.

Live evidence from 2026-09-28 shows the cost. OpenCode children of
`concord session` carried no wrapper environment: no per-Project data
directory, no restart directory. Every wrapper-started child carried both.
Those sessions wrote to the host's global OpenCode database, the wrapper's
restart had no supervising loop, and the wrapper could not resume them by
session id.

CD-0093 D5 named this gap and named Project configuration in the store as
the surface if it must change. The evidence changed the answer on one point:
the thing that varies is the host machine's entry point, not the Project.
The wrapper is a host fact of the same kind CD-0182 put in the plugin
options. This record moves the setting there.

## Decision

### D1. The option is `host_command` in the Concord plugin tuple options

The operator names the host launch command as a `host_command` argv array in
the options of the Concord plugin tuple in host OpenCode config, beside the
CD-0182 `session_opener`. Concord reads it from the `opencode debug config`
document the registry probe already parses. The installer preserves plugin
options on reinstall. Concord gains no config file and no store field.

Absent, the host command is the bare `opencode` executable, and behavior is
unchanged from CD-0093 D4 as first accepted.

### D2. The registry the session verifies is the one the command resolves

A bare `opencode debug config` probe reads the option. When the option is
present, a second probe runs through the configured command in the same
resolved directory, and its document must carry an identical `host_command`.
The agent registry check reads that second document. Launch uses the same
command. A wrapper may change the environment that selects host config, so a
registry verified through the bare host may not be the registry that runs.
CD-0093 D2 holds: the verification constrains the host that executes.

### D3. The value is argv, and a bad value refuses

The value is a non-empty JSON array of non-empty strings. Concord runs it as
argv with no shell and no placeholders, and appends its fixed arguments. A
present but malformed value, or a mismatched second document, refuses the
launch or probe with a diagnostic naming `host_command`. Nothing starts
through a fallback command. CD-0049 D4 admits no degraded start.

### D4. Every Go-core host invocation uses it

The Product/work launch, the Project-path launch, and the registry probe
shared by `concord session` and session-prepare all run the resolved
command. One resolution per start keeps the probe and the launch on one host
for every entry: the launcher TUI, `concord zl`, the CD-0182 opener, and
`concord_work_start`. The Project-path launch gains the bootstrap probe; it
carries no agent registry to check.

Adapter worker dispatch and the move-session version diagnostic stay on the
bare host. Worker lanes are not operator-resumable sessions.

### D5. The law moves with the code

This record amends CD-0093 D4: the host command is the operator's configured
command when one is named, and the bare host otherwise. It amends CD-0093
D5: the gap closes through host configuration, not the store, because the
wrapper varies per host, not per Project. A wrapper started after selection
in the resolved directory derives any per-Project environment itself. It
updates the CD-0176 D3 restatement to match.

`OPENCODE_BIN` stays a test seam and acquires no operator meaning. Concord
ships no wrapper name and hard-codes none; the setting is operator data.

## Costs

- A configured command adds one probe to each start: the bare probe that
  reads the option, and the second probe through the command.
- The probe through a wrapper runs that wrapper's own start and exit
  behavior. A wrapper that is slow to start adds that cost once to every
  session start, before the launch itself runs through the wrapper.
- A wrapper that resolves a different `host_command` than the bare host
  names refuses the start. The operator fixes the wrapper's config; Concord
  does not guess.

## Alternatives considered

- **Verify through the bare host and launch through the wrapper.** Rejected:
  it reopens the verified-versus-executing registry gap CD-0093 D2 closed.
- **Read the host config file directly and skip the bootstrap probe.**
  Rejected: config merging is host logic, and the registry probe already
  relies on the host's merged output.
- **An environment variable.** Rejected: CD-0093 D5 rejects it, a long-lived
  shell never sees a later change, and it is invisible in host config.
- **Project configuration in the store.** Rejected: the wrapper varies per
  host, not per Project.
- **A shell command string.** Rejected: it needs a shell and quoting rules;
  the argv form matches `session_opener` and the no-shell rule.
- **A placeholder template.** Rejected: Concord owns the argument order, and
  no current need goes past a fixed prefix.
- **Launch-only, with the probe left on the bare host.** Rejected: it leaves
  the probe on another host.

## Consequences

Sessions started from the launcher, `concord zl`, or the session opener run
under the operator's wrapper: they land in the wrapper's data location, the
wrapper's in-TUI restart has its supervising loop, and the wrapper can
resume them by session id. An operator who names nothing sees today's
behavior.

The registry the identity steps verify is the document the configured
command resolves, so a wrapper cannot run a different agent registry than
the one the session asserted against.

## Verification

- `go test ./cmd/concord/ -run TestResolveHostCommand` proves the
  resolution: the default, the tuple read, the verification through the
  configured command, and every refusal.
- `go test ./cmd/concord/ -run 'TestSession|TestProjectSession'` proves the
  launches: the bare launch and single probe without the option, the
  configured launch through the fake wrapper with the bare host never
  starting, and the refusal that starts no host.
- `go test ./cmd/concord/ -run TestSessionPrepareVerifiesTheConfiguredCommandDocument`
  proves the shared probe on the session-prepare side.
- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-knowledge-index.py`,
  `python3 scripts/generate-knowledge-index.py --check`,
  `python3 scripts/check-knowledge-closure.py`, and
  `python3 scripts/check-cd-allocation.py --no-fetch` pass.
- `TestSessionLaunchesAConfiguredHostCommand` anchors the live acceptance:
  after release, the operator's host config names its wrapper, and one
  `concord zl` session runs under the wrapper's data location and relaunches
  through the wrapper's restart.
