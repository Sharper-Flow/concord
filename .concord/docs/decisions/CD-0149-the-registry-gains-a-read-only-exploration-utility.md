# CD-0149: The registry gains a read-only exploration utility

- **Status:** Accepted
- **Date:** 2026-09-14
- **Scope:** The closed utility registry; generated utility tools and bodies;
  the installer agent file list; issue Add a read-only exploration utility to
  the agent registry
- **Approval:** The operator approved this bounded registry change.
- **Related:** CD-0017, CD-0081, CD-0102, and CD-0045
- **Preserves:** The worker authority boundary; the utility distinction from
  lane evidence; fail-closed tool and command permissions
- **Supersedes:** Nothing

## Context

The registry has one utility, `ci-wait`. Its generator gives every utility the
same bash-only tool projection and the same GitHub polling body. That shape
cannot represent a repository exploration utility without granting edit tools
or copying the utility outside the registry.

Repository exploration needs read and search tools. It also needs a small
read-only Git command set for history and diff questions. Exploration returns
plain text to its parent. It does not carry a packet, report schema, evidence
obligations, Concord state authority, or nested worker capability.

## Decision

### D1. The utility definition owns its tool allowance

Each utility declares `allowed_tools` beside its allowed bash commands and wall-
time cap. The generator projects every known host tool as enabled only when its
name appears in that allowance. Unknown tool names fail generation.

`ci-wait` keeps only `bash`. The new `explore` utility allows `bash`, `read`,
`glob`, `grep`, and `execute`. Edit, write, patch, task, web, Concord, and other
tools remain disabled for `explore`, so it cannot change a repository file or
record Concord state.

`execute` reaches the host tool interpreter, which carries the search tools
exploration needs. The host configures what else that interpreter exposes, and
this registry cannot narrow it. The read-only guarantee this record makes
therefore covers the repository and Concord state. It does not cover every host
tool an operator connects.

### D2. The generator owns one body template per utility

The manifest owns utility facts that other projections consume. The generator
owns the body prose because no second consumer needs to parse that prose. The
`ci-wait` body remains its current terminal CI procedure. The `explore` body
requires a bounded question, read and search steps, source-backed findings,
explicit paths and line ranges, and plain-text output.

### D3. The registry admits `explore` v1

The utility has purpose `Inspect a repository and return bounded source-backed
findings.` Its bash allowance is `git diff *`, `git log *`, `git show *`, `git
status *`, `git ls-files *`, and `git rev-parse *`. Its wall-time cap is 600
seconds. It has no packet, report schema, or evidence obligation.

### D4. Utility execution stays outside worker evidence

The adapter keeps the existing coordinator-only utility admission. A utility
returns its command result to its parent and cannot record workflow state,
start a nested worker, or discharge a lane report. The generated agent remains
hidden from the operator agent cycle.

## Consequences

- The manifest digest moves, and all generated projections regenerate.
- The installer ships `concord-explore.md` with the existing utility and lane
  definitions.
- The adapter receives a typed utility entry without a new dispatch path.
- A future utility must add a manifest allowance and a generator body template.
- Host tool exposure is not contract-declarable. A utility that allows `execute`
  inherits whatever the host connects, so the manifest states the allowance and
  the host owns the reachable set.

## Verification

- `python3 scripts/generate-agent-lanes.py --check` proves every generated
  projection matches the manifest and templates.
- `python3 scripts/check-agent-contracts.py` validates the closed registry and
  the adapter projection.
- The generator tests prove per-utility tool projection and the read-only
  exploration body.
- The Go race suite proves the generated registry remains consumable by the
  store.
