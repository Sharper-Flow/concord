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

[`scripts/predecessor-harvest.py`](../scripts/predecessor-harvest.py) performs
the harvest without an agent session:

```sh
python3 scripts/predecessor-harvest.py \
  --project <project_id>=<absolute Project path> \
  --out <absolute snapshot path>
```

It reads two sanctioned surfaces and parses no predecessor state files. Changes
and terminal counts come from the predecessor's status CLI, run with the Project
directory as the working directory. Wisdom comes from the predecessor's
read-only MCP server, spawned over stdio with the same working directory, which
is what pins Project identity. Both scale past the host tool output budget that
truncated the v1 capture.

Reflections are not captured. The two sanctioned reads disagree on the count, so
the reader records the gap in `producer` and refuses to write until
`--accept-gaps` is given.

An agent-transcribed harvest remains possible for a Product small enough to
transit whole, but it is no longer the procedure.

Harvest the Projects of the Product being migrated, at migration time — not
the whole predecessor. A snapshot needs only the Projects in scope; the
contract requires at least one.

Derivation rules, pinned by the v1 capture and reused unchanged:

- **Active changes** are open in-flight entries that have not completed every
  gate. Work in execution migrates; an entry with all gates complete is history.
- **completed_gates** derive from the first incomplete gate against the
  predecessor's gate sequencing (proposal, discovery, design, planning,
  execution, acceptance, release). The reader refuses when the entry's gate
  progress and its first incomplete gate disagree.
- **archived/closed totals** come from the status CLI's terminal counts.
- **Wisdom entries** carry `recorded_at` from the predecessor's promotion
  timestamp; `change_id` is the originating change, empty for none.
- **Reflections** carry per-entry friction and suggestion counts.

Record every capture gap in the snapshot's `producer` field. The reader does
this, and refuses to write a gapped snapshot unless `--accept-gaps` is given.

Validate before use:

```sh
concord predecessor inventory <<< '{"snapshot_path": "<absolute snapshot path>"}'
```

The inventory validates the snapshot fail-closed and enumerates it. A snapshot
that fails validation is not importable.

The inventory report is also the preflight inventory CD-0097 D2 requires. Its
`surfaces` block names every mode surface with a classification, a count, and
the structural capture gap where one exists:

- **specifications** — excluded. The sanctioned harvest does not capture them;
  they arrive as source material through the knowledge procedure.
- **active_work** — included. It is the surface `predecessor import` moves.
- **terminal_history** — excluded. The snapshot carries totals only; the
  validated snapshot stays the historical record.
- **wisdom** — excluded. It migrates as curation input.
- **reflections** — excluded. They migrate as research source material.

Run the inventory before any import. A surface the inventory does not
enumerate does not migrate, and an import request naming a surface outside the
mode refuses before import starts.

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

### Parallel mode mechanics

The import carries CD-0097's replay and refusal rules for every run. A
Product under the bounded parallel mode relies on them, because the
predecessor stays writable and keeps the live authority until cutover.

- **Surfaces.** `surfaces` names the requested surfaces. An omitted list means
  `active_work`, the only importable surface. A surface outside the mode's
  five-surface set refuses typed before import starts. A mode surface that
  migrates by its own route refuses typed with that route: specifications
  arrive through the knowledge procedure, terminal history stays in the
  validated snapshot, wisdom moves through curation, and reflections arrive
  as research source material.
- **Idempotent replay.** A repeated run with an unchanged harvest returns a
  typed no-op report: `no_op` is true and every work entry carries
  `already_imported`. A newer harvest imports newly selected changes as
  deterministic new effects. Every migrated item keeps one identity across
  harvests: the deterministic work id, `external_ref: advance:<change_id>`,
  the `predecessor-migrated` tag, and the `operator:predecessor-import` actor.
- **Harvest drift.** The predecessor keeps moving under the mode. A selected
  change that turned terminal or left the active set since the last harvest
  reports `already_imported` when its import exists. The same drift on a
  first run keeps the typed refusal, because terminal and absent changes are
  not active work.
- **Conflicts refuse typed.** A Concord-side change to an imported item is a
  conflict, not an input. The next run refuses before any write, names the
  owning source (`advance:<change_id>`), and reports the foreign event and
  actor. The migration never merges or overwrites either side. Drop the
  change from `select_change_ids` until cutover, or record the cutover
  decision for the Product.

## After import

- **Freeze the predecessor for that Product.** Nothing enforces this — the
  predecessor has no per-Project lock. The operator stops creating work there,
  and in-flight predecessor sessions for that Product finish or are abandoned
  before the harvest that precedes import. A Product declared under the
  bounded parallel migration mode is exempt from the freeze: CD-0097 keeps the
  predecessor installed, writable, and usable as the live authority until the
  operator records cutover.
- **Fix forward.** A migrated Product does not roll back. Defects become
  Concord work items.
- **Curate wisdom.** The harvest carries the Product's wisdom entries; the
  receiving curation follows the knowledge-formalization procedure — entries
  become `lesson` records or are dropped with recorded reasons. An
  unprocessed document is not law regardless of origin.
- **History stays in the frozen snapshot.** Archived and closed changes are
  deliberately not imported on the default route. The validated snapshot file
  is the Product's historical record. Under CD-0097's parallel mode,
  terminal history may import into Concord as a read-only record that carries
  no workflow authority; that import is the mode's bounded extension, not a
  default.

## Related

| Surface | Role |
|---|---|
| [`rollout-plan.md`](./rollout-plan.md) §4 | Migration policy (demand-driven, CD-0082). |
| [`decisions/CD-0097-bounded-parallel-predecessor-migration.md`](./decisions/CD-0097-bounded-parallel-predecessor-migration.md) | The bounded parallel migration mode that exempts a declared Product from the freeze. |
| [`contracts/predecessor-snapshot.schema.json`](../contracts/predecessor-snapshot.schema.json) | The fail-closed snapshot contract. |
| `concord predecessor inventory` | Snapshot validation and enumeration. |
| `concord predecessor import` | The import verb with delivered safeguards. |
| [`knowledge-formalization-procedure.md`](./knowledge-formalization-procedure.md) | Wisdom curation into `lesson` records. |
