# Predecessor migration runbook

> **Status:** Operational procedure for the delivered importer. Guidance for a
> correct operation, not a gate: migration stays demand-driven under
> [CD-0082](./decisions/CD-0082-migration-is-demand-driven.md), and nothing in
> this document is a mandated checklist, shadow requirement, or migration
> record.
> **Authority:** [`rollout-plan.md`](./rollout-plan.md) §4 owns the policy;
> `predecessor inventory` (#296) and `predecessor import` (#304) own the
> machinery; this document records how to operate them.

## Scope

One Product at a time, when a concrete workflow need fires. All Projects in
the Product move together; only deliberately selected active work moves; the
predecessor stays authority for Products not yet migrated; a migrated Product
fixes forward in Concord and does not roll back.

## Harvest

A harvest is a host-side read of the predecessor's sanctioned tool surface
(`adv_change_list` with `includeArchived`/`includeClosed`, `adv_wisdom_list`
with `project_only`, `adv_reflection_list`, each with `target_path` set to the
Project root). Nothing parses predecessor state files. Any agent session can
execute a harvest by following this procedure; the output must validate
fail-closed against `contracts/predecessor-snapshot.schema.json`.

Harvest the Projects of the Product being migrated, at migration time — not
the whole predecessor. A snapshot needs only the Projects in scope; the
contract requires at least one.

Derivation rules, pinned by the v1 capture and reused unchanged:

- **Active changes** are in-flight entries whose phase is non-terminal
  (planning or earlier). An in-flight entry at a terminal phase counts as
  history, not active work.
- **completed_gates** derive from the phase per the predecessor's gate
  sequencing (proposal, discovery, design, planning, execution, acceptance,
  release).
- **archived/closed totals** derive as the `includeArchived`/`includeClosed`
  totals minus the in-flight total.
- **Wisdom entries** carry `recorded_at` from the predecessor's promotion
  timestamp; `change_id` is the originating change, empty for none.
- **Reflections** carry per-entry friction and suggestion counts.

Record every capture gap in the snapshot's `producer` field. The v1 capture
proved the limits of this path: host tool output budgets truncated large
listings (wisdom partial for `advance`, `pokeedge`, `pokeedge-web`;
reflections counted but not enumerated; one project's totals lost to a
timeout). Per-Product harvests of small Projects transit completely. A
Product whose listings exceed the transit budget needs a scripted sanctioned
reader before it can migrate — build it when that Product's migration is
demanded, not before.

Validate before use:

```sh
concord predecessor inventory <<< '{"snapshot_path": "<absolute snapshot path>"}'
```

The inventory validates the snapshot fail-closed and enumerates it. A snapshot
that fails validation is not importable.

Validated example: the Corded per-Product capture of 2026-08-30 (1 active
change, 23 archived, 0 closed, 4 wisdom entries, 11 enumerated reflections)
validated through the inventory verb on first use.

## Import

`predecessor import` is an operator verb. It creates the Concord Product, the
full Project set, and only the selected active work, with structural
provenance (`external_ref: advance:<change_id>`, the `predecessor-migrated`
tag, the `operator:predecessor-import` actor), idempotent re-runs, and
partial-Product refusal.

Operation order:

1. **Declare the Concord side**: `product_id`, `display_name`, stage maturity
   and audience commitment; the full project assignment (each snapshot
   `project_id` → Concord `project_id` + display name, exactly one primary
   role).
2. **Select active work**: `select_change_ids` names snapshot change ids the
   enumeration shows as active. Nothing else moves.
3. **Rehearse** against a scratch store: `CONCORD_DB_PATH=<scratch path>
   concord predecessor import` with `dry_run` first, then without, and inspect
   the report. The scratch store is disposable; the real store is not.
4. **Import** against the real store. The report returns totals and per-work
   provenance.
5. **Verify**: re-run the same import. An idempotent report (`already_imported`
   totals, no new work) plus a `predecessor inventory` diff against the
   selection is the completion evidence.

## After import

- **Freeze the predecessor for that Product.** Nothing enforces this — the
  predecessor has no per-Project lock. The operator stops creating work there,
  and in-flight predecessor sessions for that Product finish or are abandoned
  before the harvest that precedes import.
- **Fix forward.** A migrated Product does not roll back. Defects become
  Concord work items.
- **Curate wisdom.** The harvest carries the Product's wisdom entries; the
  receiving curation follows the knowledge-formalization procedure — entries
  become `lesson` records or are dropped with recorded reasons. An
  unprocessed document is not law regardless of origin.
- **History stays in the frozen snapshot.** Archived and closed changes are
  deliberately not imported. The validated snapshot file is the Product's
  historical record; importing terminal work would be a new demand-driven
  extension, not a default.

## Related

| Surface | Role |
|---|---|
| [`rollout-plan.md`](./rollout-plan.md) §4 | Migration policy (demand-driven, CD-0082). |
| [`contracts/predecessor-snapshot.schema.json`](../contracts/predecessor-snapshot.schema.json) | The fail-closed snapshot contract. |
| `concord predecessor inventory` | Snapshot validation and enumeration. |
| `concord predecessor import` | The import verb with delivered safeguards. |
| [`knowledge-formalization-procedure.md`](./knowledge-formalization-procedure.md) | Wisdom curation into `lesson` records. |
