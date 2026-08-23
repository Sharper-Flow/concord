# CD-0063: Concord ships an always-on operator conduct corpus

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** Ownership and delivery of agent conduct rules for projects that
  install Concord; central installation with project-held pointers; the boundary
  between shipped conduct and repository engineering law; issues #409, #410, #413
- **Approval:** Operator accepted the drafted decision as written on 2026-08-23;
  the public record is
  [issue #410 comment](https://github.com/Sharper-Flow/concord/issues/410#issuecomment-5387585623)
- **Related:** CD-0034, CD-0043 (D1, D2, D3),
  [`../development-authority.md`](../development-authority.md)
- **Preserves:** CD-0043 D1 and D2; the reserved `skills/` boundary under
  CD-0043 D3; the closed lane registry and its digest; `agent-lane-packet.v1`
  and `agent-lane-report.v1`
- **Supersedes:** nothing

## Context

Concord agents run under host instruction the project does not author. A lane
dispatched at this repository receives its generated lane definition, the
repository `AGENTS.md`, the operator's global `AGENTS.md`, and every file the
operator lists in `instructions[]`. Measured here, 641 of those lines are
injected and bound to nothing, and 124 of them instruct behaviour this
repository forbids.

Two properties of the host make this unfixable from inside a project.
`instructions[]` concatenates across configuration layers and the schema offers
no negation, so a project can add instruction but never remove it. Agent
definitions carry no instruction field, so an agent cannot scope what reaches
it either.

The consequence is that Concord can only improve what a lane receives by
authoring instruction of its own and getting it loaded, and it can only make
that instruction accountable by getting it recorded.

CD-0043 D2 named `CONCORD_HOST_INSTRUCTIONS` the legal channel for methodology
reaching a worker. That channel binds files for provenance. It does not load
them, and it is an environment variable an operator must remember to export.
Nothing today puts Concord-authored conduct in front of a Concord agent.

## Decision

### D1. Concord owns agent conduct, not agent methodology

Conduct is how an agent addresses the operator and what it must establish
before it asserts. Ask one question at a time. Write so the question stands
alone. Verify before claiming a thing is done. Remove what a change supersedes.
These hold for any project, need no host tooling, and change rarely.

Methodology remains what CD-0043 D1 made it: how a reviewer chooses what to
look at, how a verifier chooses which commands constitute proof. That stays
host-owned and unshipped.

The distinction is portability. Conduct that names no tool and no repository is
correct wherever Concord is installed. Methodology is host-coupled by
construction, and shipping it would make Concord the owner of prose it cannot
verify.

### D2. The installation is central; a project holds pointers

Concord installs its agent definitions, its skills, and its conduct corpus once,
under the versioned data root. A consuming project stores a reference, never a
copy. One installed version is the single source for every Concord project on
the machine.

The three surfaces reach a session differently, and the difference is not
cosmetic.

Skills take a configured path list, so a pointer is native. They load on demand
through the `skill` tool, with only a name and description in the prompt until
an agent chooses the body, so a skill visible outside a Concord project costs
nothing. The installer already registers this path and continues to.

Instructions accept absolute paths, so a pointer is native here too. They load
unconditionally, so this surface is registered per project rather than globally.
A relative entry resolves by walking up from the project and yields nothing when
absent; an absolute entry always loads. Only the project-scoped registration
keeps an always-on corpus out of unrelated projects.

Agents have no path key. They are discovered from fixed directories or
redirected wholesale by environment, so no per-project pointer exists. The
installer therefore owns the central OpenCode agents directory, which is the
location `computeHostPromptProvenance` already prefers over the project copy. An
agent definition is inert until invoked by name, so central visibility carries
none of the always-on leakage that governs instructions.

`skills/` stays reserved and empty under CD-0043 D3. This decision changes where
Concord's own definitions live, not whether Concord ships a skill.

### D3. The corpus is authored, never copied

Rules are written for this corpus in Concord's own voice. Existing host rule
files are not admissible as source: they carry dated identifiers from private
predecessor history, and they name tooling that will not exist for a consumer
who installs Concord. Both are barred by the repository content rules.

A rule enters the corpus only if it is true without reference to a tool, a
machine, or a predecessor.

### D4. Conduct ships; repository law stays

`AGENTS.md` keeps what is specific to building Concord — the store connection
invariant, Go style, generated artifacts, knowledge closure, and the
prohibitions. It does not restate a shipped rule, and the corpus does not
restate repository law. One rule has one home.

### D5. Loaded means recorded

A corpus file that reaches an agent is bound by content hash in the dispatch
provenance manifest as `instruction_file`. Installing rules that a lane reads
but the record omits would reproduce the defect this decision exists to close,
and would weaken the CD-0034 guarantee that a silent injection change is
visible in dispatch evidence.

## Consequences

- A Concord project receives the corpus by reference. Upgrading the installation
  changes every project at once, with no per-project edit.
- A project that is not a Concord project receives no conduct instruction,
  because the always-on surface is registered per project rather than globally.
- Corpus content is fixed per release under the data root and reproducible from
  the artifact.
- Pointers must survive an upgrade. Referencing a version-pinned path would
  break every project on release, so the installer owns a stable path and
  repairs it. `install.py` rejects a redirected skills path today, and that
  guard must be revisited rather than removed.
- The installer gains managed state in two further locations: the central agents
  directory and each consuming project's configuration. Uninstall reverses both,
  and a project that outlives the installation degrades to no conduct rather
  than to a dangling reference.
- Operator instruction that contradicts the corpus still loads, because the
  configuration can only add instruction and never remove it. The corpus
  competes on clarity, not authority, and this decision does not claim
  otherwise.
- Conduct changes ship on the release cadence rather than immediately.

## Rejected alternatives

**Register the corpus globally.** One edit covers every project. Rejected
because always-on instruction would reach projects that do not use Concord and
never consented to it. Scope is the requirement, not reach.

**Populate `skills/` with the corpus.** The delivery rail is already built and
registered. Rejected because skills load on demand, so a conduct rule would
apply only when an agent elected to read it, which is the opposite of what
conduct means.

**Copy the corpus into each project.** Visible beside the code it governs and
reviewable in place. Rejected because copies drift from the installed version
and reintroduce the per-project maintenance this decision removes.

**`CONCORD_HOST_INSTRUCTIONS` alone.** Legal today under CD-0043 D2 and needs no
decision. Rejected because it records without loading, points at mutable host
files outside the release, and depends on an operator exporting a variable. It
remains the channel for host-authored methodology.

**Copy the operator's existing rule corpus.** Rejected under D3. It would import
private predecessor history and host tool coupling into a public release.

## Verification

- `docs/decisions/CD-0063-shipped-operator-conduct-rules.md` exists and is
  indexed exactly once in `docs/concord-knowledge-index.v1.json`.
- `skills/` still contains only `README.md`.
- `contracts/agent-lanes.v1.json` keeps its version and
  `contracts/agent-lanes.digest` is unchanged.
- A test installs, asserts the project pointer and the central agents directory,
  uninstalls, and asserts both are reversed.
- A test proves an upgrade leaves an existing project pointer resolving to the
  new version without editing that project.
- A test proves a project without the pointer receives no corpus instruction.
- A test proves the dispatch provenance digest moves when a corpus file changes.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`,
  `python3 scripts/check-public-content.py`, and
  `python3 scripts/check-cd-allocation.py` pass.
