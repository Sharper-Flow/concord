# Linear integration operations

Concord keeps Linear setup behind the existing store and client owners. The
configuration format is `linear-setup.v1` in
[`contracts/linear-setup.v1.schema.json`](../contracts/linear-setup.v1.schema.json).

## Preflight

Build one configuration with remote UUIDs for each Product team and repository
Project. Include the four distinct status IDs. Use synthetic values in tests:

```json
{
  "schema_version": "linear-setup.v1",
  "workspace_id": "workspace-example",
  "teams": [{"product_id": "product-example", "team_id": "team-example"}],
  "projects": [{"project_id": "project-example", "linear_project_id": "linear-project-example", "team_id": "team-example"}],
  "status_policy": {
    "completed_id": "status-done-example",
    "cancelled_id": "status-canceled-example",
    "superseded_id": "status-superseded-example",
    "duplicate_id": "status-duplicate-example"
  }
}
```

Run `concord linear-setup` with the object above nested under `config`. The command validates local identity bindings, reads the remote
inventory, and refuses when a configured UUID is absent. It performs no remote
write.

## Product isolation

Pass `product_id` to `linear issue-enqueue` and `linear outbox-drain`. Concord
derives the work item's Product from its Project membership and refuses a
mismatch before it writes the outbox or calls Linear. Health reports only that
Product's pending operations and linked issues.

The outbox stores its Product binding in migration 79. Legacy rows with no
unique Product remain outside scoped drains until an operator assigns them.

## Migration readback

Migration input must list each issue identity explicitly. Preserve the remote
UUID, then refresh the identifier and URL after a remote team move. Do not clone
issues, delete the original team, or infer a Project from a display name.
