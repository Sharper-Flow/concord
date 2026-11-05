---
{
  "id": "PM6",
  "title": "Reported delivery and historical note locators",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": [],
  "criterion_bindings": [
    {
      "criterion": 1,
      "exemption": "Reported-link completion and execution disposal are mutation properties, not query-corpus cases. Integrated completion and upgrade preservation evidence must cover this boundary."
    },
    {
      "criterion": 2,
      "exemption": "Historical locator preservation is a read property. Integrated evidence must cover exact home, path, commit, content hash and lesson-tag preservation."
    },
    {
      "criterion": 3,
      "exemption": "Typed historical uncertainty is a read and resolution property. Integrated evidence must distinguish missing, ambiguous and unreachable outcomes without rewriting a locator."
    }
  ]
}
---
# Concord Reported Delivery and Historical Note Locators (PM6)

## Context

Completion records the delivered outcome, not a second publication operation.
Existing historical notes remain readable through their recorded Git locators.

## Contract

Sections 1 through 4 govern completion, knowledge ownership and historical reads.

## 1. Decision

Completion accepts the existing typed reported-delivery assertion and its links.
Concord does not verify delivery by reading Git or GitHub. The existing evidence,
independent Review, applicable approvals, expected-version, authorization,
idempotency and durable-commit requirements remain required.

Completion creates no canonical work note, publication intent, compaction link,
reviewed closure hash, publication judgement or cleanup obligation. There is no
publish/reconcile operation, separate step claim/settlement protocol, or deferred
terminal cleanup. Completion atomically retains the terminal current-state work
identity and reported delivery link while disposing its workflow step records.
It retains no post-delivery step history or replacement execution archive.
Operations, effect receipts and successful-request idempotency remain with their
own retry and reconciliation consumers under CD-0242 D2.

The breaking upgrade uses the existing verified-snapshot route before removing
retired publication, claim, closure, cleanup and outside-repair state. It also
disposes completed execution that has no canonical note or cannot be compacted.
Upgrade creates no note, Git operation, successful workflow verdict or new
archive. It preserves active work, setup, historical `archived_work` locators,
their `content_hash` and `lesson_tags`. No new cleanup pass is required after upgrade.

Reusable lessons follow the independent ordinary reviewed pull-request route in
[CD-0026 D1–D2](../decisions/CD-0026-learning-capture.md).
That route requires neither work compaction nor a completion note.

## 2. Knowledge home

A Product's designated member Project and knowledge locator own shared law under
[CD-0200](../decisions/CD-0200-product-knowledge-resolves-through-one-designated-git-home.md).
Designation is explicit typed configuration, never inferred from discovery,
the current directory, or work membership. A Project may designate zero or one
of its typed Git locators as its knowledge locator. Product-home designation
names a member Project and that locator; it is not a copied path or repository URL.

Each Project's own knowledge stays in its `.concord/` tree. Additional member
Project knowledge sources require explicit registration. Historical work notes
retain their recorded home even when Product designation or membership changes.

## 3. Historical locator

The historical `archived_work` reader retains this locator:

```text
home_project_id
home_locator_id
note_path
commit_oid
content_hash
```

Project and locator IDs identify the logical repository across path, remote,
clone and archive changes. The path identifies the file in the recorded commit;
`content_hash` identifies the note bytes. Mutable paths and remotes resolve
when a consumer opens the note, and never replace these
identity fields. A historical note hash is not a
workflow closure hash.

Existing work metadata, including `lesson_tags`, terminal state, terminal time,
frozen Product/Project/Domain/tag scope and any canonical successor, stays
readable without a new publication or backfill requirement. Legacy
`component_ids` maps to `domain_ids` with the retained IDs.

One work ID resolves to at most one canonical historical note.
Historical readers return the stored locator without probing Git; the stored
commit, path, work identity and note hash identify the note. A returned locator
proves what the retained record stores, not a current Git check. They never
infer a different note or silently choose between two valid notes claiming one
work ID. Verifying the recorded commit and note hash against Git belongs to the
consumer, never to the read. Read inspection does not
publish, reconcile, prune or change membership. Restore returns the retained
historical projection rows as current state.

## 4. Read outcomes and authority

Historical reads decide from the retained record. Resolving the recorded
commit against a Git repository, including its absence or unreachability,
belongs to the consumer, never to the read.

| Outcome | Meaning |
|---|---|
| canonical locator | the reader returned the single retained canonical locator; its stored commit, path, work identity and note hash identify the note, and no Git object was checked |
| `not_compacted` | no retained canonical note is indexed for the work identity; this does not prevent completion |
| `ambiguous` | multiple retained notes claim the work ID |
| `missing` | the retained locator names a Git object the resolving consumer did not find; the read itself claims nothing about the object |

The historical read computes no authority tier of its own: an OK read is
authoritative over the retained record, and a store that cannot answer refuses
as `unreachable` rather than guessing. The envelope levels `authoritative`,
`degraded` and `unreachable` are [TS7](agent-result-envelope.md) vocabulary; no
historical read sets `degraded`, because it returns the retained record whole
or refuses.

An offline or lost remote does not prove absence. Historical Product visibility
uses frozen associations, not later home-Project memberships. A renamed, moved or
read-only home does not rewrite a locator. A consumer may use another clone or
backup to resolve the recorded commit and verify the hash.

## Acceptance criteria

- Given accepted delivery and the other current completion requirements
  When completion commits
  Then terminal identity and reported links survive without a Git/GitHub read,
  publication operation, closure hash or retained workflow step records.

- Given existing historical work and lesson tags
  When upgrade, restore or a historical read runs
  Then its recorded locator, note content hash and lesson tags remain unchanged.

- Given conflicting or incompletely retained historical note identity, or an
  unreachable Git authority while resolving a retained locator
  When the historical reader or the resolving consumer answers
  Then it returns typed uncertainty rather than guessed authority or absence.

## Verification

Completion and upgrade need mutation witnesses; historical preservation and
uncertainty need read witnesses. Criterion bindings identify those boundaries,
not a claim of executed integration evidence.

Criterion 2's integrated locator-preservation evidence is unmet. Upgrade
preservation of the full `archived_work` row — home IDs, note path, commit OID,
`content_hash` and `lesson_tags` together — is proved by
`TestTerminalStepsRetirementVerifiedSnapshotUpgrade`
(`internal/store/workflow_terminal_steps_migration_test.go`).
`TestPublishedExecutionAbsenceAuthorizationAndWire`
(`internal/agent/terminal_absence_test.go`) checks only the home IDs and note
path on the historical read. No test covers locator preservation across
restore, or the commit OID, `content_hash` and `lesson_tags` on the read.
