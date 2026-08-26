# CD-0079: Directory-to-Project resolution is an operator read

- **Status:** Accepted
- **Date:** 2026-08-26
- **Scope:** A read-only `project-resolve` CLI verb, its authentication
  posture, and its output shape
- **Approval:** Operator selected the unauthenticated read verb with a
  decision record on 2026-08-26, after review of the CD-0059 scope objection.
  The pull request is the public record.
- **Related:** CD-0008 (D1), CD-0078, CD-0059, CD-0021, CD-0005, issues #316,
  #533
- **Preserves:** CD-0021's rejection of a second write authority, CD-0005's
  separation of the agent tool surface from CLI inventory, the grant and
  capability machinery on the agent surface

## Context

Concord could not answer "which Project owns this directory" for any caller
that asked it directly.

`Store.ResolveProject` computes the answer. It reads `remote.origin.url`,
normalizes it, matches `project_locators`, falls back to the canonical path,
and refuses with a typed failure when nothing matches. Three production sites
reach it, and none is a read the caller requested for its own sake: grant
issuance, dispatch ambient verification, and the knowledge home lookup.

`concord_product_view.resolve` does not call it. That operation reads `QueryQ1`
against the `ambient_project_id` the grant machinery already resolved. The
resolution happens upstream of the read, so the only operator route to the
answer was the `grant` response, which requires a registered client key and a
signed assertion.

CD-0078 places terminal placement in the host and expects host launchers to
show Concord state beside their own rows. Minting a grant to ask which Product
a directory belongs to is not a proportionate answer to that question.

A host cannot compute the answer itself. CD-0008 D1 states that a path or a
remote is never Project identity, only replaceable evidence for a stable
Project ID. A host that joins on a directory basename invents an identity
Concord deliberately does not hold.

## Decision

### D1. `project-resolve` is a read-only core CLI verb

The verb takes a directory, and optionally a worktree that defaults to the
directory. It returns the resolved Project ID, the Product IDs that own it,
the membership `scope_version`, the repository's canonical path and normalized
remote, whether the working tree is the main checkout, and the locators that
carried the match.

It carries the Product hop because a Project ID alone does not name the Product
whose state a host wants to display. It carries `scope_version` because a host
cache must distinguish a repository that moved on disk from Product-to-Project
authority that changed.

An unregistered repository is the typed `KindUnknownScope` refusal the store
already produces. The verb never guesses, and it never writes a locator to make
a lookup succeed. Registration remains the operator's explicit act through
`project locator-add`.

Placement follows `worktree-locate` (issue #316): a core CLI verb because the
inputs are registered locators only the core can read, and a host script would
duplicate database access or double-hop through this verb anyway.

### D2. The verb does not authenticate its caller

No operator verb authenticates the operator. `worktree-locate` and
`predecessor-inventory` are already unauthenticated reads, and the launcher
renders portfolio state through the store with no grant at all. Concord's trust
boundary for the operator surface is filesystem access to the authority
database.

CD-0059 rejected a CLI verb that placed "an agent-reachable authority path
outside the grant and capability machinery". That objection transposes to a
read as a loss of `product_read` scope enforcement: any local process could
exec this verb and learn a directory-to-Project-to-Product mapping its grant
does not cover.

This decision accepts that consequence, for one reason. A process that can exec
`concord project-resolve` can open the SQLite file the verb reads. The grant
machinery is scope discipline for the agent tool surface, not a confidentiality
boundary against local processes, and treating it as one here would buy no
protection while denying hosts a proportionate read.

The reasoning is specific to reads and does not extend. CD-0059's rejection
still governs authority paths, and D3 keeps the write boundary intact.

### D3. The verb stays out of the agent surface

`project-resolve` is not added to `contracts/agent-tool-surface.v1.json`, the
adapter, or the generated manifest. CD-0005 invariant 2 derives the agent
surface from accepted jobs and never from CLI inventory. An agent needing this
answer already receives it as `ambient_project_id` in its envelope.

The verb appends no event and opens no second authorization path into work
creation, so CD-0021's rejection of an operator authoring surface is untouched.

## Consequences

- A host launcher can ask which Product a directory belongs to with one call,
  and can cache the answer against `scope_version`.
- An unregistered repository produces a refusal a host can act on, which makes
  missing registration visible instead of silent.
- Any local process can read the directory-to-Project-to-Product mapping. D2
  records this as accepted, not overlooked.
- The `--help` output bound of 8192 bytes now has about 780 bytes of headroom.
  A later verb may need that bound revisited, which is a deliberate signal that
  the operator CLI is not an open catalogue.

## Rejected alternatives

**Gate the verb behind an interactive-terminal check.** Rejected because
host-script integration is the whole purpose. The launcher requires an
interactive terminal because a human reads it. This verb exists for a program.

**Gate the verb behind a durable identity-assertion event, as `concord session`
does.** Rejected because `session` mints identity for a session that will act.
This verb answers a question and returns. Recording an event for every host
refresh would make a read into a write.

**Return the Project without the Product hop.** Rejected because the host's
question is which Product's state to display. Withholding the hop forces a
second call for no reduction in exposure, since the same caller could make it.

**Add the resolution to the agent tool surface instead.** Rejected because an
agent already receives the resolved ambient Project in its envelope, and
CD-0005 invariant 2 forbids deriving agent surface from CLI inventory.

**Leave hosts to the `grant` verb.** Rejected because it requires a registered
client key and a signed assertion to answer a question that writes nothing, and
because it produces a durable grant record as a side effect of asking.

## Verification

- `TestProjectResolveAnswersDirectoryToProjectAndProductHop` proves D1's output
  shape against a real git repository and registered locators.
- `TestProjectResolveRefusesUnregisteredRepository` proves the typed refusal and
  that no payload is written on refusal.
- `TestProjectResolveWritesNothing` proves D3's read-only claim by comparing
  durable counts across `domain_events`, `agent_grants`, `agent_approvals`,
  `agent_approval_challenges`, `idempotency_records`, and `durable_operations`.
- `TestProjectResolveRequiresDirectory` proves the verb refuses an absent or
  empty directory.
- `TestRunHelpListsExactCommandFormsAndStdinShapes` and
  `TestCommandRouterAcceptsCanonicalAndTwoWordFormsWithoutPanicking` cover the
  verb's registration and both invocation forms.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  `python3 scripts/check-law-coverage.py`, and
  `python3 scripts/check-cd-allocation.py` pass.
