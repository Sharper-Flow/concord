# Concord development authority

**Status:** Accepted under CD-0089, amended by CD-0121.
**Approval date:** 2026-09-08.
**Approval:** Operator approval for [issue #955](https://github.com/Sharper-Flow/concord/issues/955).

## Context

Concord owns its development workflow. Planning authority follows the Product's
selected mode, not a global issue-provider requirement. A local-only Product can
use a private Git repository without Linear or GitHub Issues.

[CD-0121](decisions/CD-0121-product-scoped-planning-authority.md) amends the
planning and issue-linkage clauses of CD-0010 and CD-0089. Their repository
review, merge, isolation, and Product-law boundaries remain in force.

## Contract

The operator selects Linear-enabled or local-only operation for each Product.
Concord records the selected authority and the evidence for its activation.
The choice applies to the Product, not the installation, Project, or repository
path. An ambiguous Product context requires an explicit operator choice before
an agent creates planned work.

This contract defines policy. It does not claim that Product-mode configuration,
Linear setup, or synchronization is implemented by the installed software.

### Authority by fact type

| Fact or action | Authority | Evidence |
|---|---|---|
| Planned work and defects for a Linear-enabled Product | Linear | Confirmed issue identity and the Product's selected destination |
| Planned work and defects for a local-only Product | Concord | Durable local work identity and intent |
| Workflow transitions, verdicts, and completion | Concord | Session identity and typed operation records |
| Review and merge | Repository pull requests and required checks | Review decisions, check results, merge record, and changed files |
| Implementation isolation | Git branches and worktrees | Branch ancestry, worktree boundaries, and commits |
| Product law | Accepted decisions, specifications, and constitutional documents | Canonical knowledge records, approved versions, and law relations |
| Predecessor lessons | Public predecessor records | Reachable citations in [advance-predecessor-lessons.md](advance-predecessor-lessons.md) |

### Product modes

For a Linear-enabled Product, an agent creates operator-directed future-work
issues in Linear. A linked local work item can carry execution and coordination
state. It is not a second independent authority for the same planning fact.
A queued or failed creation request is not a confirmed Linear issue.

For a local-only Product, Concord's local database owns planned work and defects.
The Product requires no Linear connection, credentials, or API access. A GitHub
issue is not a prerequisite for a local work item or development session.
Code and selected documents can use a private Git repository. That choice does
not authorize publication of the database, credentials, sessions, or every
generated document.

Products with different modes can share one installation. A Linear-enabled
Product does not impose its destination, credentials, or publication rules on
another Product. An unavailable Linear connection does not change the affected
Product to local-only operation.

### Activation and cutover

A requested mode and an active integration are different facts. Before Linear
activation, establish the Product identity, intended workspace and destination,
authorized access, and a supported route for the requested operations. Record
the operator's activation decision and its evidence. Missing setup is reported
as missing setup, not represented by invented configuration or successful writes.

Concord's own Product has Linear-enabled operation as its selected target.
Its existing GitHub-planned development retains that declared authority until
an explicit, verified cutover. This transition does not create a permanent
GitHub mode or authorize an automatic fallback after Linear activation.

Existing issue links remain traceable. A cutover identifies which records
remain in their existing system and which migrate, with explicit identity
mapping. It does not automatically duplicate issues or reinterpret historical
review and merge evidence.

Host-owned instructions and installed integration capabilities must agree with
the activated policy. A repository edit alone does not change host authority.
Conflicting host rules require a change through their owning source and rollout,
not a live-file patch or an agent bypass.

### Preserved boundaries

1. Planning records do not silently amend accepted Product law.
2. Concord's public repository retains public pull-request and required-check
   evidence. A local assertion is not proof of merge.
3. Implementation uses an isolated branch and worktree, not the default checkout.
4. A session records its Concord identity and the authoritative planning
   reference appropriate to its Product mode. A local work identity satisfies
   planning linkage for local-only operation.
5. Advance remains reference-only. Concord writes no predecessor state.
6. Replacement readiness is an evidence claim. It neither blocks Concord
   development nor activates an integration or migration.
7. Public repository content excludes private Product data, credentials, and
   inaccessible private-source citations. Public review describes the change
   without copying private planning records.

Document placement, synchronization protocols, and credential mechanisms require
their own approved contracts. Choosing Linear does not transfer workflow or
Product-law authority to it, or require every document to live there.

## Acceptance criteria

- Given a Product with an established planning mode
  When an agent records planned work or a defect
  Then its mode determines the planning authority, without amending Product law.

- Given a claim that a Concord change merged
  When the claim is checked
  Then the public pull request and required checks supply the merge evidence.

- Given implementation work
  When an agent writes it
  Then it resides in an isolated branch and worktree, not the default checkout.

- Given a proposed change that conflicts with accepted law
  When review examines it
  Then the conflict requires an explicit law amendment rather than silent narrowing.

- Given a development session with a resolved Product
  When Concord records its context
  Then the session has a Concord identity and a mode-appropriate planning reference.

- Given a workflow transition, verdict, or completion claim
  When the operation succeeds
  Then Concord records the operation in its durable authority.

- Given a replacement-readiness claim
  When the claim is checked
  Then the accepted readiness evidence determines it without activating Linear.

- Given Concord development work
  When the workflow records its state
  Then Concord writes no Advance state.

- Given two Linear-enabled Products and a third local-only Product
  When an agent records future work for the local-only Product
  Then it needs no Linear credentials or GitHub issue and exposes no other Product data.

- Given a Linear-enabled Product and an operator-directed future-work request
  When Linear confirms issue creation
  Then the agent reports its confirmed identity rather than only a local record.

- Given a Linear-enabled Product whose connection is unavailable
  When an agent cannot confirm issue creation
  Then it reports pending or failed creation without switching planning authority.

- Given a requested Linear mode without verified setup
  When an agent checks integration readiness
  Then it reports the missing setup and does not claim an active integration.

- Given existing GitHub-planned work and an approved Linear cutover
  When records migrate
  Then explicit identity mappings preserve existing links without automatic duplicates.

## Verification

These are process-authority requirements, not evidence of implemented Linear
behavior. Knowledge, document, link, and public-content checks validate the
policy artifacts. Review checks the authority table, mode boundaries, and
cutover requirements against CD-0121.

Runtime activation requires separate evidence for Product configuration, access,
creation, identity mapping, and isolation. A passing document validator does not
prove those capabilities. Existing workflow, repository-review, and isolation
mechanisms retain their own verification contracts.

- Criterion 1: review the authority table against CD-0121 D1. Runtime routing
  requires separate evidence; this amendment establishes its policy owner.
- Criterion 2: a merge claim requires the public pull request and required
  checks. Document-link validation alone is not proof of a particular merge.
- Criterion 3: branch and worktree evidence must establish isolation. Existing
  worktree contracts own that runtime proof; this amendment preserves them.
- Criterion 4: review the explicit amendment and its law relations.
  `scripts/check-knowledge-index.py` validates the declared knowledge graph.
- Criterion 5: existing session identity evidence remains required. Local and
  Linear planning linkage need mode-specific implementation evidence.
- Criterion 6: typed workflow records supply transition and verdict evidence.
  The generated agent contracts and store workflow tests retain that scope.
- Criterion 7: the accepted floor and `scripts/check-floor-readiness.py` own
  readiness evidence. They do not establish a Product's Linear activation.
- Criterion 8: `scripts/check-predecessor-independence.py` checks repository-owned
  agent surfaces. This amendment adds no predecessor write route.
- Criterion 9: review the local-only boundary in Product modes. Credential
  independence and cross-Product isolation still require runtime tests.
- Criterion 10: a provider-confirmed issue identity must support a creation
  claim. This policy amendment contains no Linear call or creation result.
- Criterion 11: review the outage rule for unchanged planning authority.
  Queue and failure behavior require separate integration tests.
- Criterion 12: activation requires Product configuration and access evidence.
  Policy adoption and document checks do not supply that evidence.
- Criterion 13: a migration requires its own approved contract and verified
  identity mapping. This amendment executes no migration.
