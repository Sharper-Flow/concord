# CD-0171: Initiatives map to Linear Projects

- **Status:** Accepted
- **Date:** 2026-09-23
- **Scope:** How a Linear-enabled Product maps Concord Initiatives, repository
  Projects, and work items onto Linear, and how pull requests link to Linear
- **Approval:** The operator approved this mapping in-session under
  [Concord (CON) issue 422](https://linear.app/sharper-flow/issue/CON-422), after
  research recorded under [CON-425](https://linear.app/sharper-flow/issue/CON-425).
- **Related:** CD-0041, CD-0121

## Context

Concord maps a Product to one Linear team, each repository Project to one
Linear Project, and each work item to one Linear issue. No mapping carries a
Concord Initiative to Linear.

Linear lets an issue belong to one Project only
([Linear Projects](https://linear.app/docs/projects)). The repository mapping
fills that one field, so an epic-level Initiative has no place in Linear. A
Linear Initiative groups Projects, not issues
([Linear conceptual model](https://linear.app/docs/conceptual-model)).

The operator ranked the views. The Product view comes first. The next view
shows the major Initiatives across the repositories of one Product. The
repository view ranks below both.

## Decision

### D1. A Product maps to one Linear team

This mapping does not change. The team is the Product view.

### D2. An Initiative maps to a Linear Project

Each Concord Initiative has one Linear Project. The issue of each entry sets
that Project. The Initiative narrative becomes the Project description. The
team Projects page then lists every Initiative of the Product, with progress
across all repositories.

Concord owns entry order. The Linear order of issues is not authoritative.

### D3. A repository maps to a team label

The `project_ids` map from repository Project to Linear Project is removed.
Each issue carries one label for each repository Project of its work item.
One Product is one team, so a team label reaches every repository of that
Product. A work item that spans two repositories carries both labels.

### D4. Work outside an Initiative has no Linear Project

An issue whose work item is in no Initiative has an empty Project field. The
repository label still identifies its repository.

### D5. Requiredness uses one label

An optional entry carries the `optional` label. Every other entry is required,
which matches the Concord default. Linear has no native field for this fact.

### D6. The first Initiative owns a shared entry

Concord allows a work item in several Initiatives. Linear allows one Project.
The Initiative that the work item joined first sets the Project. The issue
description links the other Initiatives. When that entry leaves its
Initiative, the next oldest Initiative sets the Project.

### D7. Linear Initiatives stay outside the mapping

Concord does not create or change Linear Initiatives. The operator may group
Initiative Projects under a Linear Initiative. The import verb reads a Linear
Project into a Concord Initiative. The import from a Linear Initiative is
removed.

### D8. A pull request names its Linear issue

The body of each Concord pull request carries `Related to <issue key>`. This
relation phrase links the pull request without a status change
([Linear GitHub integration](https://linear.app/docs/github)). A closing
phrase such as `Fixes` is not used, because the Concord outbox is the only
writer of issue status. GitHub Issues Sync stays off for a Linear-enabled
Product, because Linear is its planning source under CD-0121.

## Migration

1. Each linked issue gets the label of its repository Project.
2. Each issue leaves its repository Linear Project.
3. Each issue of an Initiative entry moves to the Linear Project of that
   Initiative.
4. The operator archives the repository Linear Projects. Concord does not
   delete them.

## Alternatives rejected

- **An Initiative maps to a Linear Initiative.** A Linear Initiative holds
  whole Projects. With repositories as Projects, it counts every issue of each
  repository, not the entries of the Initiative.
- **An Initiative maps to a parent issue.** This keeps the repository view,
  which the operator ranks last. A parent issue has no milestones or project
  updates.
- **Each repository maps to a sub-team.** Every issue changes team, and each
  sub-team needs its own status mapping.
- **Nested Linear Initiatives above repository Projects.** This keeps the
  count defect of the first alternative, and it needs the Linear Enterprise
  plan ([Linear sub-initiatives](https://linear.app/docs/sub-initiatives)).

## Consequences

- The Linear outbox gains Project operations for Initiatives.
- Repository identity moves from a Project field to labels. A repository view
  in Linear is a label filter.
- Implementation, migration, and pull request linkage are outstanding under
  [CON-427](https://linear.app/sharper-flow/issue/CON-427).
