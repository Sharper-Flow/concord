# CD-0093: Session start resolves the Project directory, not the host command

- **Status:** Accepted
- **Date:** 2026-09-01
- **Scope:** What `concord session` resolves before it starts the host; the
  working directory of a launcher-started session; issue #661
- **Approval:** The operator raised the question on 2026-09-01 after a
  launcher-started session ran against the wrong repository. The pull request is
  the public record.
- **Related:** CD-0078, CD-0031, CD-0049, CD-0088, issue #661
- **Preserves:** CD-0078 D1's host ownership of placement, CD-0031's
  core-derived session boot, CD-0049 D4's refusal of a degraded start

## Context

The launcher spans a portfolio. An operator opens it from one directory and
selects work that belongs to any Project in the Product.

`concord session` does not carry that selection into the host. It builds
`argv := []string{"opencode", "--agent", handle, "--prompt", prompt}` and runs
it with the inherited environment and the inherited working directory
(`cmd/concord/session.go:234`, `cmd/concord/session.go:169`). The session
therefore opens wherever the launcher was started, which is the selected work's
repository only by coincidence.

The other host-start path disagrees. `concord invoke` bootstrap passes `--dir`
from its input (`cmd/concord/work_bootstrap.go:273`). One product starts the
host two ways with two different answers about where the work lives.

Concord holds the fact both paths need. `project_locators` stores a
`canonical_path` locator for each Project (`internal/store/schema.go:731`).

Issue #661 raised a second question with the first: whether `concord session`
should also resolve which host command to run, so an operator entry point can
prepare a per-Project host environment after selection. CD-0078 settled the
adjacent placement question and this record answers the remaining two.

## Decision

### D1. The session directory comes from the selected work's Project

`concord session` resolves the canonical path of the Project that owns the
selected work and starts the host in that directory. A session for work in one
Project never opens in another Project's repository.

The mechanism is the child process working directory, not a flag. The
interactive host command accepts no directory flag; `--dir` belongs to the
non-interactive `run` subcommand that `cmd/concord/work_bootstrap.go` uses. The
two paths reach the same outcome through the surface each one has.

### D2. Resolution fails closed

A missing Project, an absent `canonical_path` locator, or a path that does not
resolve refuses the launch with a typed diagnostic. No session starts in a
fallback directory.

CD-0049 D4 already admits no degraded start for agent identity. A session that
silently opens in the wrong repository is the same failure with a slower
symptom, because the agent's file operations land somewhere the operator did
not choose.

### D3. The host command stays fixed

`concord session` starts a fixed host command. It reads no operator setting
naming a different program.

CD-0078 rejected a pluggable terminal-placement strategy as "an abstraction with
one real implementation on one machine." A configurable host command is the same
shape one layer down, and the same objection answers it. The released binary
runs the host the same way on every machine.

### D4. The per-Project host environment is an open gap, not a silent one

D3 has a cost and this record names it rather than leaving it to be discovered.

An operator whose host entry point prepares a per-Project environment cannot
reach it through the Concord launch path. That entry point runs before the
operator selects work, so it cannot key on a Project that is not yet chosen, and
D3 declines to invoke it afterward. Launcher-started sessions therefore run with
whatever host environment the launcher inherited.

If evidence later shows this must change, the surface is Project configuration
in the store, because the thing that varies is per-Project. It is not an
environment variable, and it is not `OPENCODE_BIN`, whose only consumers are
`cmd/concord/work_bootstrap.go`, `adapter/opencode/concord.ts`, and the adapter
test that sets it to a fake binary. That name is a test seam and acquires no
operator meaning here.

## Consequences

- A launcher-started session operates on the repository that holds the selected
  work, so the agent's file tools reach the right tree.
- The two host-start paths agree about the working directory.
- A Project with no canonical path cannot start a session until it has one,
  which surfaces incomplete Project registration at launch rather than later.
- An operator host environment that varies by Project does not reach
  launcher-started sessions. D4 records this and no code hides it.
- CD-0078's consequence about `ResolveProject` is unchanged. That gap runs from
  directory to Project; this record runs from Project to directory and uses the
  store rather than opening an operator read path.

## Rejected alternatives

**Honor `OPENCODE_BIN` in `concord session`.** Rejected because it is a test
seam. Its non-test consumer sets it to a fake binary, and promoting a test hook
to operator configuration gives one name two meanings that drift apart.

**Add a configurable host command now.** Rejected under CD-0078's reasoning
about one implementation on one machine. The gap D4 names is real, and a
decision can reopen it when the evidence covers more than a single host.

**Prepare the host environment upstream and inherit it.** Rejected because it
cannot work. The entry point runs before selection, so it cannot key on the
Project the operator has not chosen. Inheriting one Project's environment for
another Project's work is the defect D1 removes, moved to a different layer.

**Set the directory from the launcher's own working directory.** Rejected
because it is the current behavior and it is what issue #661 reports.

## Verification

- A session for work whose Project resolves to a canonical path starts the host
  in that path.
- A session whose Project has no resolvable canonical path refuses with a typed
  diagnostic and starts no host.
- The session argument vector names the fixed host command and carries no
  operator-supplied program name.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.
