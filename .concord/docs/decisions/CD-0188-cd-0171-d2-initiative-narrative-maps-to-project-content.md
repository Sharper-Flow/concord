# CD-0188: CD-0171 D2 maps the Initiative narrative to Project content

- **Status:** Superseded by CD-0213
- **Date:** 2026-09-28
- **Scope:** The Linear field CD-0171 D2 names for the Initiative narrative
- **Amends:** CD-0171 D2 at its third sentence ("The Initiative narrative
  becomes the Project description.")
- **Preserves:** CD-0171 D1 and D3 through D6 in full, and every other D2
  sentence: one Linear Project per Initiative, the entry issues set that
  Project, the team Projects page lists every Initiative, and Concord owns
  entry order
- **Related:** CD-0171,
  [Concord (CON) issue 463](https://linear.app/sharper-flow/issue/CON-463)
- **Approval:** The operator approved the work item contract that carries this
  record on 2026-09-28.

## Context

CD-0171 D2 says "The Initiative narrative becomes the Project description."
Linear gives a Project two text fields: a short description, and a content
markdown doc
([Linear Projects](https://linear.app/docs/projects/create-and-manage)).

The shipped mapping writes the narrative to the content field. The Linear
client's Project type documents `Content` as the narrative target and
`Description` as "Linear's legacy project text, which a project without
content may hold"
(`internal/linearclient/client.go`). The `projectCreate` and `projectUpdate`
mutations both send `content`, and the narrative-revision path queues a
`project_update` that carries the revision to the Project content
(`internal/store/linear_integration.go`).

Written law and shipped behavior therefore name different fields. A reader of
D2 alone expects the narrative in the short description field, and the shipped
sync never writes it there. The code is correct: the content doc holds long
markdown, which is the shape the Initiative narrative has.

## Decision

### D1. The narrative maps to the Project content

CD-0171 D2's sentence "The Initiative narrative becomes the Project
description" reads instead: the Initiative narrative becomes the Project
content. The Project description holds only legacy text a Project created
before this mapping carried; the sync neither writes nor flattens it.

No other D2 sentence changes. Each Concord Initiative still has one Linear
Project, the issue of each entry still sets that Project, the team Projects
page still lists every Initiative of the Product, and Concord still owns entry
order.

## Alternatives considered

- Change the sync to write the narrative into the short description field and
  keep CD-0171 D2 as written. Rejected: the description field is short plain
  text and the narrative is long markdown, so the content field is the
  correct target; this would also flatten Projects that already carry the
  narrative in content.

## Consequences

The written law and the shipped Linear sync name the same field, and a reader
of CD-0171 D2 with this record gets one answer.

No store, client, or test behavior changes. Projects created before this
record keep their legacy description text untouched, and their content
carries the narrative from the record's date forward.

## Verification

- `internal/linearclient.TestCreateProjectSendsClientUUIDAndContentFields`
  proves the create mutation sends the narrative in the content field.
- `internal/linearclient.TestUpdateProjectAddressesTheRemoteUUID` proves the
  update mutation addresses the remote Project and carries the content field.
- `python3 scripts/check-doc-contract.py` proves this record carries the
  current decision outline and passes the writing rules.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove this record registers
  with a current content hash and no unprocessed document remains.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0188
  identifier allocates once.
