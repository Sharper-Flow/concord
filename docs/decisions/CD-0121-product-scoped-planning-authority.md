# CD-0121: Product-scoped planning authority

- **Status:** Accepted
- **Date:** 2026-09-08
- **Scope:** Product planning authority, optional Linear, local-only operation,
  development-session linkage, and integration cutover policy
- **Approval:** The operator approved this policy change in-session under
  [issue #955](https://github.com/Sharper-Flow/concord/issues/955).
- **Amends:** CD-0010's planning authority and issue-linkage requirements;
  CD-0089 D1 and D2 at those same boundaries
- **Preserves:** Concord workflow authority, accepted Product law, repository
  review and merge evidence, worktree isolation, and predecessor independence

## Context

A mandatory external issue provider prevents a Product from using Concord with
local storage and a private Git repository. An installation-wide choice also
forces unrelated Products to share an integration requirement.

The operator selected two supported forms: a Product can use Linear for planned
work, or retain local planning authority in Concord. When Linear is enabled,
operator-directed future-work issues belong there. Optional integration must
not become an optional obligation to honor the selected destination.

## Decision

### D1. The Product owns the planning choice

Linear is optional per Product. The operator selects the Product's mode, and
the agent resolves that Product before choosing a planning destination.
Neither a repository path nor another Product's setup supplies this authority.

The normative mode, identity, isolation, and cutover contract is
[development-authority.md](../development-authority.md). Its authority table
replaces unconditional GitHub issue ownership for planned work and defects.

### D2. Planning authority does not absorb other authority

Concord continues to record workflow transitions, evidence verdicts, and
completion. Accepted Product law governs contracts. Repository review, merge
evidence, and Git isolation remain distinct from the planning provider.

CD-0089 still authorizes Concord to coordinate its own development. Its
GitHub-only planning and session-linkage clauses, and the clauses it preserved
from CD-0010, yield to the Product-scoped contract. No other CD-0010 or CD-0089
authority boundary changes.

### D3. A policy decision is not deployed capability

Mode selection does not prove setup, credentials, API access, synchronization,
or successful remote issue creation. Activation requires recorded evidence and
an explicit operator decision. An outage does not authorize a change of owner.

Existing work retains traceable identities across an approved cutover. The
policy does not migrate, duplicate, or publish records by itself. Host-owned
rules require their own source change and rollout when they conflict with the
activated policy.

### D4. The amendment does not wait for full integration delivery

This decision establishes the authority contract now. Runtime integration and
live routing cutover require separate verified delivery. Document placement,
credential storage, and synchronization design remain separately scoped work.

## Rejected alternatives

**Require Linear for every Product.** This excludes the approved local-only,
private-repository use case.

**Retain GitHub Issues as a universal prerequisite.** This imposes a different
external planning provider on local-only and Linear-enabled Products.

**Treat provider failure as local-only operation.** This silently changes
authority and can create competing records for one request.

**Transfer all state and documentation to Linear.** Planning-provider selection
does not decide workflow authority or document storage.

## Consequences

Repository instructions refer to the Product-scoped contract rather than
require one issue provider for every session. Local work and external issues
need traceable identities without becoming competing planning authorities.

The amendment changes policy, not the installed integration. Public artifacts
must not expose private planning data, and private Git use does not authorize
publication of the local database or execution history.

## Verification

The criteria and their verification obligations belong to the
[development-authority contract](../development-authority.md#acceptance-criteria).
The policy artifact and knowledge records are checked for structure, links,
hashes, and public-content safety. The linked criteria state required behavior, not
a claim that the Linear runtime exists. Runtime capability and activation
require their own implementation and verification evidence.
