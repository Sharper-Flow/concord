# CD-0068: Domain-Anchored Observations

Status: accepted
Date: 2026-08-24

## Context

Issue #459. CD-0030 D1 made an observation the durable form of "I noticed
something": bounded, non-authoritative, recorded on a work item. Both durable
inter-agent channels anchor there. `work_observations.work_id` and both
endpoints of `work_messages` reference `work_items(id)`
(internal/store/schema.go:1631, 1632, 1735).

An agent that notices an architecture area lacks something holds no work item.
The channel CD-0030 opened is closed in the case that needs it most: an agent
recognizing during a session that a Domain is missing work the operator would
want to see. Capture (CD-0018) remains available, but it records an unconfirmed
opportunity as tracked work in `needed`, and CD-0030 D3 already reserves
capture as the deliberate promotion path rather than the cheap channel.

A Domain cannot hold the record itself. The `domains` table carries
`registry_content_hash` and `scanned_commit_oid`
(internal/store/schema.go:1800-1801) and the registry is pinned to a scanned
commit (internal/store/schema.go:1777). Domain identity is a Git projection,
not writable state.

## Decision

### D1. A Domain is a second observation anchor

`domain_observations` is keyed by `(product_id, domain_id)` with a foreign key
to `domains(product_id, domain_id)` and `ON DELETE NO ACTION`, matching
`domain_project_attachment_sets` and `domain_resource_attachment_sets`
(internal/store/schema.go:1879, 1899). That pattern is the accepted answer for
mutable state attached to a Git-projected Domain, and this is its third use.

The CD-0030 D1 bounds carry over unchanged: statement 1 to 512 characters,
refs at most 16, tags at most 8. The table is fold-only under the same triggers
as `work_observations`.

Rejected alternative: widening `work_observations.work_id` into a nullable
typed subject with a discriminator column. Every existing reader would then
test a case the work-anchored path never had, and the one-home invariant of
CD-0041 D3 reads more clearly when a Domain-scoped row is keyed by Domain.

### D2. The open window is bounded, and a full window refuses

A work item goes terminal and stops recording (CD-0030 D4). A Domain is
perpetual and never does, so unbounded growth replaces staleness as the failure
mode. Each Domain holds at most 64 observations in state `open`.

Recording into a full window fails with a typed refusal naming the Domain.
Eviction is rejected: dropping the oldest observation to admit a new one is the
silent drop that CD-0030 exists to prevent, and a cap that discards evidence
quietly is worse than one that reports pressure.

### D3. Dismissal is the operator's review act and requires approval

State is `open` or `dismissed`, following the two-state shape of
`work_messages`. Dismissal flips state, never deletes; the row persists for
audit. Only `open` rows count against D2.

`concord_domain.observation_dismiss` carries `ApprovalClass: required`. An
agent that could dismiss without approval could drain the window it fills, and
the approval is what makes the queue the operator's rather than the agent's.
Recording carries `ApprovalClass: none`, matching CD-0030.

### D4. Promotion is unchanged

Turning a Domain observation into work stays the CD-0018 path: capture with a
`raised_from` edge. Promotion does not dismiss the observation, and dismissal
does not promote. The two acts are independent, as CD-0030 D3 already holds for
work-anchored observations.

### D5. Non-authority is preserved verbatim

A Domain observation satisfies nothing. No evidence kind, no gate, and no
workflow action reads one as authority. It cannot approve, transition, or
discharge an obligation. CD-0030 D1 states this for work items and it binds
here without amendment.

### D6. Reads join the existing Domain surface

`concord_domain.detail` carries the open observations for the Domain, newest
first and bounded, so the operator meets them at the boundary where the next
decision about that Domain is made. This mirrors CD-0030 D2, which puts
work-anchored observations in the continuity snapshot rather than behind a
dedicated read. No new read operation is introduced.

### D7. Ordering within a Domain is out of scope

`QueryDomainActiveWork` orders by `priority, id`
(internal/store/domain_reads.go:486). Whether a Domain also needs a declared
sequence is a separate question with a separate failure mode, and CD-0041 D8
denies Initiatives Domain identity for reasons this decision does not revisit.
An observation anchor does not imply an ordering primitive.

## Consequences

- A store migration adds `domain_observations` with its fold-only triggers.
- The agent tool surface gains two operations, so the contract digest and the
  generated artifacts change; the generator owns them.
- Issue #423 blocks the useful part of this decision. A Domain observation that
  no operation reads back is worth nothing, and PR #452 supplies that read path
  for work-anchored observations first.
- The 64-row cap is a declared constant, not a derived one. Raising it is a
  later amendment, and the refusal in D2 makes pressure visible before the
  question becomes urgent.
- Nothing here creates a trackable kind, a lifecycle state, an owner, or a
  claim. CD-0009 D2 and CD-0018 D4 stand unamended.
