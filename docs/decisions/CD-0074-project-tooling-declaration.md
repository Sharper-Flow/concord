# CD-0074: A project declares its ready tooling as a validated manifest

- **Status:** Accepted
- **Date:** 2026-08-25
- **Scope:** Per-project declaration of configured quality tooling;
  `.concord/tooling.v1.json`; [`contracts/project-tooling.v1.schema.json`](../../contracts/project-tooling.v1.schema.json);
  `scripts/check-project-tooling.py`; issue #510
- **Approval:** Operator approved the surface and its boundary in the session
  that opened issue #510 on 2026-08-25; the merged pull request is the public
  record.
- **Related:** CD-0040 (D6), CD-0043 (D1), CD-0047 (unlapsed coverage lesson),
  CD-0055 (D1), [`../capability-placement.md`](../capability-placement.md) §3–§4,
  [`../predecessor-operational-coverage.md`](../predecessor-operational-coverage.md)
- **Preserves:** CD-0043 D1's host-owned methodology boundary; CD-0040 D6's
  probe-family limit; the closed lane registry and packet/report schemas
- **Supersedes:** nothing

## Context

An agent entering a Concord-managed project cannot tell which quality tools,
scanners, and check commands are already configured there. It rediscovers them
by reading `AGENTS.md` prose, guessing, or not at all. The infrequent tools
lose worst: a scanner that is configured but deliberately kept out of CI is
invisible, so it never gets used.

Nothing in Concord declares per-project tooling. A Project is a database row
plus a locator; the lane packet carries no environment field; workers report
`verification_commands` after the fact, and nothing tells them what exists
beforehand (issue #510).

## Decision

### D1. One hand-authored manifest per project holds the intent

`.concord/tooling.v1.json` declares, per tool: an `id`, a `purpose`, the
`invocation` an agent runs, a cost `tier`, an optional `config_path`, an
`in_ci` boolean with its `ci_reference`, and optional `notes`. These fields are
hand-authored because they have no upstream source to drift from. The
invocation and the sanctioned entry point are exactly the facts a filesystem
read cannot produce: both `go test ./...` and `bin/oc-test` work in this
repository, and only intent says which one is sanctioned.

### D2. Every derived claim is proved at check time

`scripts/check-project-tooling.py` validates the manifest on every CI run,
wired through `scripts/check-json.py`. It rejects unknown fields, bad tiers,
duplicate ids, duplicate JSON keys, and unsafe paths. It proves resolution:
`config_path` must exist on disk, and `ci_reference` must appear as a literal
substring in a file under `.github/workflows/`. `in_ci: false` forbids a
`ci_reference`. A missing manifest is a finding, not a vacuous pass — deleting
the declaration must be a visible decision, the same unlapsed-coverage lesson
CD-0047 records for `check-law-coverage.py`.

### D3. The manifest carries facts, never methodology

CD-0043 D1 excludes lane methodology from durable state, and this record keeps
that boundary intact. "`gosec` is configured, invoked as `gosec ./...`, on
demand, slow" is a fact about a tool and belongs in the manifest. "Run `gosec`
before reviewing crypto changes" is methodology and stays host-owned. The
manifest states what a tool is and how it is invoked; it never states when an
agent should choose it.

### D4. No new probe family

CD-0040 D6 restricts core execution to the accepted Git probes. The checker
performs file reads only: manifest JSON, workflow YAML as text, and config
path existence. Concord never executes a declared tool to test availability,
because the commands come from repository content and executing them is a new
probe family with security semantics this record does not open. Host
availability, if ever needed, arrives as an attributed report from an
authenticated trusted client under CD-0040 D6's existing channel.

### D5. CI is an attribute, not the authority

CI enumerates only the always-on subset, and infrequent tools are deliberately
outside it — that is the operator's stated case for this surface. The source of
the declaration is configuration presence plus operator intent; `in_ci` is one
derived boolean that separates "a required workflow already ran it" from "it
runs only when an agent invokes it."

## Consequences

- Every Concord-managed project may carry the manifest; this repository
  dogfoods it with six entries, including `bin/oc-test` as the sanctioned local
  entry that CI does not run.
- The schema documents shape; the checker enforces shape plus resolution,
  because existence and workflow-membership checks are inexpressible in JSON
  Schema.
- Adding a tool is a reviewed edit plus a passing check. Removing a tool's
  configuration without updating the manifest fails
  `check-project-tooling.py`.
- Other projects adopt the surface by committing the same manifest shape; the
  checker is a single file with no dependencies beyond the standard library.

## Rejected alternatives

**A generated inventory projected from the repository.** Reading CI workflows
and config files to synthesize the list was rejected: CI excludes infrequent
tools by design, and a generated artifact would restate what the declaration
plus its check already prove. The hand-authored fields are intent with no
upstream source, so generation has nothing to generate from.

**A probing inventory that executes each tool.** Rejected under CD-0040 D6:
running repo-named binaries is a new core probe family, and it answers a
question ("is it installed on this host") the manifest deliberately does not
ask.

**Storing the declaration in the Product database or the lane packet.** No
consumer requires the manifest inside durable state today; any agent can read
the committed file. Revisit with a concrete second consumer under the
capability-placement rubric, which would also require packet schema movement
that CD-0043 explicitly avoided.

**Delegating the surface to `AGENTS.md` prose.** Rejected because prose cannot
be structurally validated, drifts silently, and is already pointer-only by
`scripts/check-agents-md.py`.

## Verification

- `.concord/tooling.v1.json` exists, validates against
  `scripts/check-project-tooling.py`, and `python3 scripts/check-json.py`
  passes.
- `scripts/test-project-tooling.py` covers the failure modes (missing
  manifest, unknown fields, bad tiers, duplicate ids, unresolvable and unsafe
  paths, missing and forbidden `ci_reference`, duplicate keys, multiline
  invocations) and runs in CI.
- `python3 scripts/check-doc-links.py`, `python3 scripts/check-agents-md.py`,
  `python3 scripts/check-cd-allocation.py --no-fetch`, and
  `python3 scripts/check-knowledge-index.py` pass.
