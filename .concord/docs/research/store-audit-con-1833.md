# CON-1833 store audit: schema, indexes, triggers, shape and growth

This is the canonical CON-1833 research record. A readable Linear Document copy will be linked both ways after publication.

## Executive summary

- **Subject.** Repository commit [`24eb4378804f1ce77378a842267dfdb72679e97d`](https://github.com/Sharper-Flow/concord/commit/24eb4378804f1ce77378a842267dfdb72679e97d), and one backup copy of the live store with SHA-256 `5f5a32b77e7482a012633b6c4ee1dac614699f4bed7fb298c7623b98fbbbdc2f` (633,950,208 B, schema 191, newest row timestamp `2026-11-07T22:47:35.729975536Z`). On the backup, `PRAGMA integrity_check` returns `ok` and `PRAGMA foreign_key_check` returns 0 rows.
- **Shape.** 63 user tables (+ `sqlite_sequence`), 461 user columns (+ the 2 `sqlite_sequence` columns), no views, 110 indexes (38 explicit + 72 automatic), 169 triggers, 201 CHECK clauses, 191 applied migrations. `user_version` is 0.
- **Evidence.** The production-driver replan and snapshot queries used the backup opened read-only and immutable. Earlier authorizer attribution and plans used Python SQLite 3.46.1 on a byte-identical working copy opened read-write; no writes are asserted. See Evidence and method for provenance. An earlier benchmark on a disposable copy of the store is excluded and supports no claim here. No trigger timing is claimed; trigger cost is stated only from static plans and opcode counts.
- **Space.** 222.70 MiB (233,517,056 B) of the 604.58 MiB file is free pages. `idempotency_records` is the largest object (257.57 MiB with its indexes); its `result_payload` values alone are 195,110,211 B.
- **Hot reads.** The listed route plans include indexed live-item lookups, but shared scope validation also scans the 12-row `archived_work_products` index even for live items. Absent-item checks scan `archived_work` (415 rows); current runtime has no writer for that table. No new ticket is proposed for these scans at the observed sizes. Successful workflow-action and general planned work-item mutation response enrichment reads every cancelled instance (971 in the backup) before filtering by lifecycle and project when the changed item has an instance (R14); its driving-sessions query sorts with a temporary B-tree. Q3 (browse) and Q7 (history) sort with temporary B-trees, as expected for their result ordering.
- **Gaps found.**
  - Overlap-resolution invalidation scans two tables on every overlap resolution and every reopen.
  - 9 foreign keys whose parents are deleted at runtime lack a full child-key index: seven probes scan and two search a leading prefix.
  - Baseline summary browse runs two indexed liveness queries per returned row.
  - Several projections are written or kept without a semantic reader.
  - Replay of pre-`mutation_response` idempotency records is refused.
  - The law-declared `verifying` workflow state has no writer.
- **Recommendations.** 14 separately ticketable changes follow. R1–R13 retain their original benefit order; R14 was appended from final common-path tracing, not re-ranked. Data-changing items need operator approval for production application data and a verified snapshot first.

## Findings and recommendations

Each recommendation is one ticket. R1–R13 are ordered by benefit, highest first; R14 was appended after final common-path tracing without re-ranking. "Production data" means the operator approval class for production application data (the CON-1833 rules cite CD-0243 D2). Schema changes follow the migration and checksum rules (CD-0055, CD-0111).

### R1. Reclaim free pages and compact managed snapshots [CON-1839](https://linear.app/sharper-flow/issue/CON-1839/reclaim-concord-store-free-pages-and-compact-managed-snapshots)

- **Evidence (observed).**
  - The backup has 57,011 free pages of 154,773 (233,517,056 B, 222.70 MiB, 36.8%).
  - The CLI keeps up to 8 managed snapshots (`managedSnapshotCount = 8`, `cmd/concord/snapshot_retention.go:17`).
  - `auto_vacuum` is off: free pages are reused, but the file does not automatically shrink. The Online Backup API (`internal/store/internal/sqlitedb/snapshot.go:67-72`) copies free pages; the backup has 154,773 pages. *Inference*: up to eight snapshots in total may each carry comparable free space. Their individual free space was not measured.
- **Owner.** PM10 (product-memory-recovery) owns snapshot aging, backup topology and reclamation. The retention contract adds no `VACUUM` promise (`.concord/law/specs/compaction-retention-policy.md:92-94`).
- **Boundary.** Production data. The operator handles the live store. A verified snapshot is required before any live maintenance.

### R2. Compact duplicate idempotency `result_payload` copies [CON-1840](https://linear.app/sharper-flow/issue/CON-1840/compact-duplicate-idempotency-result-payloads-without-weakening-replay)

- **Evidence (observed).**
  - `idempotency_records.result_payload` holds 195,110,211 B across 47,270 rows (Appendix B).
  - Law allows a durable operation to own the committed result "without a duplicate response copy in the replay index". Removing that copy must preserve the original scope, request digest, result classification, evidence and the no-second-effect guarantee (`.concord/law/specs/agent-call-context-contract.md:293-297`).
  - The replay window is the lifetime of that authority, not a calendar duration, so no time-to-live (TTL) applies.
- **Not counted.** How many rows have an authoritative durable owner, which bounds the compactable subset. 195,110,211 B is the upper bound.
- **Boundary.** Production data. Preserve identity, scope, digest and the durable result owner; take a verified snapshot first.

### R3. Define and repair replay of pre-`mutation_response` idempotency records [CON-1841](https://linear.app/sharper-flow/issue/CON-1841/repair-replay-of-idempotency-records-written-before-mutation-response)

- **Evidence (observed).** 41,096 rows lack `authorized_scope_snapshot.mutation_response`, including 34,278 in current mutation pairs. `retainedMutationResponse` (`internal/agent/idempotent_response.go`) refuses such a snapshot. Every row after `2026-10-26T20:32:06.198545077Z` carries the field.
- **End state.** Replay returns the original result or the current durable operation's state as law requires, or a lawful typed absence. Deletion is not the assumed outcome.
- **Boundary.** Any rewrite or deletion of rows is production data.

### R4. Index overlap-resolution invalidation [CON-1842](https://linear.app/sharper-flow/issue/CON-1842/index-overlap-resolution-invalidation-by-work-pair-and-relation)

- **Evidence (observed).**
  - The invalidation statements at `internal/store/workflow_domain_overlap.go:96`, `:103`, `:126` and `:132` plan `SCAN relations` and `SCAN workflow_overlap_resolutions` without an index. The backup holds 3,127 resolutions and 2,783 relations.
  - They run on every overlap resolution (3,133 `resolve_overlap` idempotency records) and on reopen (`internal/store/lifecycle.go:327-358`), not only on cleanup.
- **Acceptance.** The same statements plan as index searches with `foreign_keys` on.
- **Boundary.** Schema migration and checksum rules.

### R5. Add child-prefix indexes for the 9 uncovered foreign keys with runtime parent deletes [CON-1843](https://linear.app/sharper-flow/issue/CON-1843/index-foreign-key-children-probed-during-runtime-parent-deletes)

- **Evidence (observed).** Each foreign key below is `ON DELETE RESTRICT` and has a runtime delete site on its parent. Seven child probes scan; two search a leading prefix without covering all foreign-key columns (Appendix E):

  - `workflow_overlap_resolutions`(to_work_id, to_contract_version) → `workflow_contracts`: 3,127 child rows; parent delete `internal/store/work_removal.go:440`
  - `workflow_overlap_resolutions`(from_work_id, from_contract_version) → `workflow_contracts`: 3,127 child rows; parent delete `internal/store/work_removal.go:440`
  - `workflow_overlap_resolutions`(to_work_id) → `work_items`: 3,127 child rows; parent delete `internal/store/work_removal.go:419`
  - `workflow_overlap_resolutions`(from_work_id) → `work_items`: 3,127 child rows; parent delete `internal/store/work_removal.go:419`
  - `workflow_contracts`(work_id, superseded_by) → `workflow_contracts`: 2,090 child rows; parent delete `internal/store/work_removal.go:440`
  - `workflow_contract_law_additions`(product_id, law_id, reservation_owner_work_id, reservation_owner_contract_version, home_domain_id) → `workflow_law_addition_reservations`: 116 child rows; parent delete `internal/store/work_removal.go:453`
  - `workflow_contract_law_additions`(work_id, contract_version, product_id) → `workflow_architecture_bindings`: 116 child rows; parent delete `internal/store/work_removal.go:440`
  - `workflow_law_addition_reservations`(owner_work_id, owner_contract_version) → `workflow_contracts`: 86 child rows; parent delete `internal/store/work_removal.go:440`
  - `product_knowledge_homes`(locator_id) → `project_locators`: 12 child rows; parent delete `internal/store/project_locator.go:667`
- **Acceptance.** With `foreign_keys` on, each parent delete's plan shows a child probe using an index on all foreign-key columns.
- **Boundary.** Schema migration and checksum rules.

### R6. Batch browse per-item liveness reads [CON-1844](https://linear.app/sharper-flow/issue/CON-1844/batch-per-item-liveness-reads-in-work-browse)

- **Evidence (observed).** After the page query, `QueryQ3` calls `deriveWorkLivenessQ` once per returned item (`internal/store/query.go:869-875`). Each call runs two indexed statements, plan IDs 2524 (`internal/store/work_removal.go:519`) and 2525 (`internal/store/work_removal.go:523`). A baseline summary page of N items therefore runs 2N extra liveness statements.
- **Constraint.** Keep the output semantics exactly.
- **Boundary.** No data change.

### R7. Retire the unread `workflow_backlog_alignment` writes and projection through its owning law [CON-1845](https://linear.app/sharper-flow/issue/CON-1845/retire-unread-workflow-backlog-alignment-writes-and-projection)

- **Evidence (observed).**
  - 2,050 rows, 1,880,064 B, 214 rows in the last 7 days.
  - Still written by `writeWorkflowBacklogAlignmentRowsTx` (`internal/store/workflow.go:687-698`), reached from `foldWorkflowDiagnosisRecorded` (`internal/store/workflow.go:710`, `:740`).
  - The only runtime reads are its own delete-and-replace (`internal/store/workflow.go:689`) and removal cleanup (`internal/store/work_removal.go:435`). There is no semantic reader.
  - The table is declared at `.concord/law/specs/workflow-engine-contract.md:394`. CD-0242 D2 says "A category without a consumer is not stored" (`.concord/law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md:167-169`).
  - History: [CON-1334](https://linear.app/sharper-flow/issue/CON-1334/no-read-route-exposes-backlog-alignment-facts) and [CON-1710](https://linear.app/sharper-flow/issue/CON-1710/workflow-records-written-after-pr-2317-have-no-agent-read) (both Done).
- **Boundary.** Production data; law change through the law route.

### R8. Retire the dormant `workflow_design_records` and `workflow_proposal_records` projections through owning law [CON-1846](https://linear.app/sharper-flow/issue/CON-1846/retire-dormant-workflow-design-and-proposal-projections)

- **Evidence (observed).**
  - Design records: 641 rows, 2,940,928 B. Proposal records: 506 rows, 1,339,392 B.
  - Commit [`92707e1abb8150e44d12f49edcf91e3eca008416`](https://github.com/Sharper-Flow/concord/commit/92707e1abb8150e44d12f49edcf91e3eca008416) (2026-11-04, [PR 2286](https://github.com/Sharper-Flow/concord/pull/2286)) removed their dispatch. Last rows: design 2026-11-04, proposal 2026-11-04.
  - The only remaining writers are fold handlers whose event kinds nothing constructs (`internal/store/workflow.go:1079-1095`, `:1097-1118`).
  - The only runtime reader is removal cleanup (`internal/store/work_removal.go:440`).
  - Law still lists `workflow.proposal_recorded` and `workflow.design_recorded` (`.concord/law/specs/workflow-engine-contract.md:237-238`).
- **Boundary.** Production data; law change through the law route.

### R9. Retire the duplicate `workflow_contract_law_modifications` projection after proving `workflow_contracts.law_modifies` is the sole owner [CON-1847](https://linear.app/sharper-flow/issue/CON-1847/retire-the-duplicate-contract-law-modification-projection)

- **Evidence (observed).**
  - 1,287 rows, 217,088 B, written per law id at `internal/store/workflow.go:631`. The only runtime reader is removal cleanup (`internal/store/work_removal.go:440`).
  - On the backup, `json_each(workflow_contracts.law_modifies)` yields 1,287 elements and matches the table both ways: 0 elements missing from the table, 0 rows missing from the JSON, 0 duplicates.
- **Boundary.** Production data. Prove sole ownership before removal.

### R10. Restore a supported reader for law-retained `workflow_decision_records`, or reconcile the retaining law first [CON-1848](https://linear.app/sharper-flow/issue/CON-1848/restore-a-supported-reader-for-law-retained-workflow-decision-records)

- **Evidence (observed).**
  - 5 rows, written at `internal/store/workflow.go:1075`.
  - Runtime reads are only `count(*)` for liveness (`internal/store/work_removal.go:519`) and removal cleanup (`:440`). No reader returns the record content.
  - CD-0112 D3 says "Retained decision records stay readable" (`.concord/law/decisions/CD-0112-record-delivery-exits-the-step-a-session-executed.md:39`). The table is declared at `.concord/law/specs/workflow-engine-contract.md:398`.
- **Boundary.** A reader is a code change. Changing the law goes through the law route before any data change.

### R11. Remove `runtime_state_writers` residue [CON-1849](https://linear.app/sharper-flow/issue/CON-1849/remove-the-migration-only-runtime-state-writers-residue)

- **Evidence (observed).**
  - 5 rows, 8,192 B, all `surface='knowledge'`.
  - In non-test Go the name appears only in migrations (`internal/store/schema.go:5682`, `:7544-7558`, `:8746`). It does not appear in `.concord/law` or `contracts`. No runtime reader or writer exists; only the snapshot digest reads it.
- **Boundary.** Production data; schema migration and checksum rules.

### R12. Reconcile the law-declared `verifying` workflow state, which has no current writer [CON-1850](https://linear.app/sharper-flow/issue/CON-1850/reconcile-the-law-declared-verifying-workflow-state-with-its-missing)

- **Evidence (observed).**
  - Law lists `verifying` and the transitions `running`→`verifying` and `verifying`→`completed` (`.concord/law/specs/workflow-engine-contract.md:191-203`).
  - The CHECK on `workflow_instances.instance_state` admits it, and so does the payload schema (`contracts/agent-tool-surface-payloads.schema.json:1848`). 0 rows hold it.
  - No non-test Go outside `internal/store/schema.go` writes it.
- **Constraint.** Do not simply delete the vocabulary. Decide between implementing the transition and amending the law.
- **Boundary.** Law route; schema migration rules if the CHECK changes.

### R13. Remove the 4 prefix-redundant indexes [CON-1851](https://linear.app/sharper-flow/issue/CON-1851/remove-four-prefix-redundant-store-indexes)

- **Evidence (observed).**
  - `work_removal_operations_work`, `workflow_design_records_latest`, `workflow_native_runs_work` and `workflow_proposal_records_latest`.
  - Each has key columns that are a leftmost prefix of another index on the same table (Appendix E); 69,632 B in total. Lowest benefit.
- **Dependency.** For the design and proposal indexes, follow R8: retiring their tables also retires their indexes.
- **Boundary.** Schema migration and checksum rules.

### R14. Avoid reading unrelated cancelled workflow instances in mutation responses [CON-1854](https://linear.app/sharper-flow/issue/CON-1854/mutation-responses-read-every-cancelled-workflow-instance)

- **Evidence (observed).** After a successful workflow action or general planned work-item mutation, response enrichment reads the work pin when the changed item has an instance (`internal/agent/mutations.go:653`, `internal/agent/mutations.go:1701`, `internal/store/workpin.go:77-113`). In the backup, 2,376 of 2,378 work items have instances. The count at `internal/store/workpin.go:152` uses `workflow_instances_state` to read all cancelled instances (971 in the backup) before joining and filtering by completed lifecycle and project. Statement 2716 also uses a temporary B-tree (Appendix J). No timing was measured.
- **Affected route.** Successful workflow-action responses, including completion, and general planned work-item mutation responses when the changed item has an instance; work-pin reads also execute this count.
- **Acceptance.** Return the same count without reading unrelated cancelled instances; verify correctness across projects and the plan with the production driver.
- **Boundary.** Schema migration and checksum rules if applicable; production data approval only if data changes.

### Related tickets

- [CON-914](https://linear.app/sharper-flow/issue/CON-914/reclaim-store-pages-and-clear-historical-workflow-action-replay-copies), [CON-915](https://linear.app/sharper-flow/issue/CON-915/compact-generic-mutation-replay-results-without-losing-the-missing) and [CON-916](https://linear.app/sharper-flow/issue/CON-916/account-for-raw-result-storage-in-durable-operations-before-any) were canceled on 2026-10-17. R1–R3 rest on fresh evidence and the operator's 2026-11-07 audit direction. Those tickets are not reopened.
- [CON-1076](https://linear.app/sharper-flow/issue/CON-1076/recover-committed-mutation-results-safely-after-a-lost-response), [CON-749](https://linear.app/sharper-flow/issue/CON-749/idempotency-records-have-no-retention-so-the-store-grows-without-bound), [CON-1334](https://linear.app/sharper-flow/issue/CON-1334/no-read-route-exposes-backlog-alignment-facts) and [CON-1710](https://linear.app/sharper-flow/issue/CON-1710/workflow-records-written-after-pr-2317-have-no-agent-read) are Done. They are history, not open duplicates.

### Ticket premises corrected

- **"CD-0242 D6 says each rule is checked once per write."** CD-0242 D6 is "Native Task belongs to the host" (`.concord/law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md:234-250`). It says nothing about checking a rule once per write. The nearest text is D3, "A common requirement has one implementation that every type reuses" (`:185`), which governs workflow stage requirements, not database triggers. So no law found here makes trigger-and-Go double checks a violation.
- **"`durable_operations` result kinds admit retired values."** Its CHECKs are single fixed values: `attempt_epoch=1`, `step_kind='internal_sqlite'`, `result_kind='completed'`. All 32,854 rows match. No retired value is admitted.
- **"38 indexes."** That is the explicit count. The full count is 110, including 72 automatic indexes that enforce primary keys and UNIQUE constraints.

### No recommendation is made for

- the law-retained `archived_work_domains` and `archived_work_tags`, or other `archived_work` history;
- `domain_observations` (CD-0224);
- deleting effect receipts without a documented end to the retries and reconciliation consumer of the original intent (`.concord/law/specs/compaction-retention-policy.md:87`);
- promoting JSON fields to columns (only three JSON columns are used by field or element in runtime SQL; Appendix B);
- `user_version`;
- deleting indexes with zero plan hits.

## Hot-route query plans (observed)

Production driver: `modernc.org/sqlite v1.61.0` (`go.mod:7`), which embeds SQLite 3.54.1. Connections enable foreign keys (`foreignKeys = "ON"`, `internal/store/internal/sqlitedb/connection.go:21`). Plans below were produced with that driver and `foreign_keys(1)` against the backup. A plan ID is the statement index in the extracted statement set. Appendix J contains the actual plan lines. The earlier plan set used Python SQLite 3.46.1 with foreign keys off; the replan used SQLite 3.54.1 with foreign keys on.

**Result.** The listed live-item lookups use indexes or the integer primary key, except the shared scope query's scan described below. An index search can still read a broad range: 2714 reads all cancelled instances before filtering (R14). The optional browse law-context read has no extracted plan. `SCAN s` (Q3) and `SCAN entries` (Q7) read the statement's own CTE co-routine, not a table. Temporary B-trees also appear for Q3, Q7, session-resume identity 2449, memberships 261, completed exits 2697 and work-pin driving sessions 2716.

**Shared invoke boundary.** Invocation admission includes 14 (`internal/store/agent_authority.go:204`), 213 (`internal/store/project_locator.go:439`), 219 (`internal/store/project_locator.go:506`) and 209 (`internal/store/project_locator.go:112`). Statement 209 runs in `Authorize`, again in `validateRuntimeScope`, and in `AuthorizeTx` for mutations, when those stages are reached. After preceding admission succeeds, `validateRequestedScope` (`internal/agent/runtime.go:303,367-379`) calls `ProductsForWorkIDs` for every invocation naming work IDs, for reads and mutations alike: resume, session resume, browse with `work_ids`, work pin, dispatch and completion. For each ID it runs 2452 (`internal/store/terminal_absence.go:10`); if execution is absent, 2443 (`internal/store/scope.go:28`) scans `archived_work`. It then runs 2444 (`internal/store/scope.go:43`) even for live IDs: its plan scans `archived_work_products_lookup` (12 table rows) and uses temporary B-trees for DISTINCT and ORDER BY. No ticket is proposed for 2444 given the observed small population and no runtime writer for archived products in the extracted inventory; future schema or runtime changes could change that judgement.

**Absence paths.** After 2452 finds no live item, 2453 (`internal/store/terminal_absence.go:30`) scans `archived_work` through `sqlite_autoindex_archived_work_1` and sorts with a temporary B-tree. If no outcome is found, 2454 (`:58`) performs `SELECT EXISTS` on `linear_issue_links` using its primary key; it does not scan `archived_work`. These checks are called from workflow resume (`internal/store/workflow_resume_read.go:56`), session resume (`internal/store/session_resume_read.go:15`), work pin (`internal/store/workpin.go:78,142`), relations trace (`internal/agent/runtime_read_handlers.go:317` → `QueryQ8`, `internal/store/query.go:1025` → `requireWork`, `:911`), actionable-work checks including dispatch (`internal/store/planning_retirement.go:24`, `internal/agent/mutations.go:514`), work-item summary (`internal/store/agent_reads.go:27`) and operation application (`internal/store/operation.go:372`). `QueryQ3` browse does not call `publishedWorkAbsenceQ`; browse with `work_ids` has the shared boundary 2452/2443/2444 described above. For absent execution, project retirement (`internal/store/project_handoffs.go:914`) additionally executes 2455 (`internal/store/terminal_absence.go:84`), which scans `archived_work` twice, before the ordinary absence path. These are conditional paths, not scans on every call. The 415-row `archived_work` has no runtime writer at this commit (Appendix D); current runtime therefore does not grow it, but future schemas or external changes may. No scan ticket is proposed at the observed size.

**Mutation-scope distinction.** General planned mutations can repeat 2452/2443/2444 during preflight (`internal/agent/mutations.go:1202,1211-1215`) and transactional authorization (`:1630,1739,1243-1247`). Workflow actions return through their separate path at `:1165-1167`; their `mutateWorkflowAction` and transaction authorization (`:506-548,631-642`) do not call those two general-mutation derivations. Their shared invoke-boundary scope check still runs.

The table covers the listed shared paths and explicitly enumerated action branches, not every possible workflow action or every statement in their folds and enforcement. Statements can be conditional on reaching their source location; inserts, trigger programs and other unlisted effects are not enumerated here.

| Route (entry point) | Plan IDs: statement locations |
|---|---|
| Resume — `readWorkflowResumeTx`, `internal/store/workflow_resume_read.go:49` | 2756 (`internal/store/internal/sqlitedb/revision.go:11`), 2446 (`internal/store/scope.go:91`; only if 2698 returns no row), 2452–2454 (`internal/store/terminal_absence.go:10,30,58`; 2453–2454 conditional on absence), 2698 (`internal/store/workflow_resume_read.go:60`); optional 2699 (`internal/store/workflow_resume_read.go:117`) |
| Session resume — `ReadSessionResume`, `internal/store/session_resume_read.go:61` | revision 2756 (`internal/store/session_resume_read.go:80`, `internal/store/internal/sqlitedb/revision.go:11`), then 2452 with 2453–2454 conditional on absence (`internal/store/session_resume_read.go:15`, `internal/store/terminal_absence.go:10,30,58`), then 2446 (`internal/store/session_resume_read.go:18`, `internal/store/scope.go:91`), regardless of instance presence, when each is reached; 2449 (`internal/store/session_resume_read.go:25`), 2450 (`internal/store/session_resume_read.go:90`); resume set if an instance exists; active contract 2621 (`internal/store/workflow_contract_authority.go:14`), 2623 (`internal/store/workflow_contract_authority.go:24`), called at `internal/store/session_resume_read.go:103`; handoffs 207 (`internal/store/project_handoffs.go:1048`), 208 (`internal/store/project_handoffs.go:1071`), 193 (`internal/store/project_handoffs.go:634`) |
| Browse — `QueryQ3`, `internal/store/query.go:549` | revision 2756 (`internal/store/internal/sqlitedb/revision.go:11`); product 235 (`internal/store/query.go:226`); page query 246–253 (8 filter variants, `internal/store/query.go:584`); memberships 261 (`internal/store/query.go:891`); blockers 260 (`internal/store/query.go:849`); per-item liveness 2524 (`internal/store/work_removal.go:519`), 2525 (`internal/store/work_removal.go:523`) at `internal/store/query.go:869-875`; Linear links 255 (`internal/store/query.go:735`); full intent 256 (`internal/store/query.go:772`); optional `detail=full` with `work_ids`: active contract 2621/2623, handoffs 208/207, 2695 (`internal/store/workflow_read.go:29`), law context (no plan), 137 (`internal/store/native_runs.go:200`) |
| History — `QueryQ7`, `internal/store/q7_current.go:59` | main query 232 / 233 (`internal/store/q7_current.go:117`) |
| Dispatch — `ApplyWorkflowActionTx`, `internal/store/workflow_dispatch.go:60`, via `mutateWorkflowAction`, `internal/agent/mutations.go:506` | 2452–2454 (`internal/store/terminal_absence.go:10,30,58`; 2453–2454 conditional on absence), 175 (`internal/store/planning_retirement.go:28`), 15 (`internal/store/agent_mutation_boundary.go:62`), 2639 (`internal/store/workflow_dispatch.go:224`), 2686 (`internal/store/workflow_plan_storage.go:15`), 2699 (`internal/store/workflow_resume_read.go:117`), 2697 (`internal/store/workflow_records.go:17`); actor lookup 2588 (`internal/store/workflow_action_guards.go:52`, called at `internal/store/workflow_dispatch.go:153`); operation update 2590 (`internal/store/workflow_action_guards.go:116`, called at `internal/store/workflow_dispatch.go:175`); non-completion finish 2638 (`internal/store/workflow_dispatch.go:92`, called at `:197`); post-apply enrichment as below, before idempotency update 19 (`internal/store/agent_mutation_boundary.go:115`, called at `internal/agent/mutations.go:666`); action-specific: contract-approval fold 2536 (`internal/store/workflow.go:554`) or 2621 (`internal/store/workflow_contract_authority.go:14`), 2623 (`internal/store/workflow_contract_authority.go:24`), 2545 (`internal/store/workflow.go:776`), 2704 (`internal/store/workflow_takeover.go:66`), 2562 (`internal/store/workflow.go:1305`), 2550 (`internal/store/workflow.go:972`), 2575 (`internal/store/workflow.go:1418`), 2576 (`internal/store/workflow.go:1425`), 2569 (`internal/store/workflow.go:1388`); replay 170 (`internal/store/operation_result.go:40`) |
| Completion — `applyCompleteWorkflowActionTx`, `internal/store/workflow_action_guards.go:179`, plus `internal/store/workflow_completion.go:62` | 2621 (`internal/store/workflow_contract_authority.go:14`), 2623 (`internal/store/workflow_contract_authority.go:24`); 2617 (`internal/store/workflow_completion.go:62`); 2618 (`internal/store/workflow_completion.go:72`) (non-research types only); 2640 (`internal/store/workflow_dispatch.go:354`), 2641 (`internal/store/workflow_dispatch.go:359`), 173 (`internal/store/operator.go:74`), 2534 (`internal/store/workflow.go:457`), 2619 (`internal/store/workflow_completion.go:122`), 2620 (`internal/store/workflow_completion.go:144`), 2592 (`internal/store/workflow_action_guards.go:235`), 2593 (`internal/store/workflow_action_guards.go:240`) |

**Mutation post-apply enrichment.** On successful workflow-action and general planned work-item mutation paths, `internal/agent/mutations.go:653,1701` calls `enrichMutationPayloadTx` (`:1352`). `readMutationWorkPinTx` runs the absence check 2452–2454 (`internal/store/workpin.go:78`, with 2453–2454 conditional), version 2710 (`:82`), and, for a live item, instance existence 2712 (`:104`). When the changed item has an instance, `ReadWorkPinTx` repeats the absence check 2452–2454 (`:142`), then reads identity 2713 (`:145`), cancelled-instance count 2714 (`:152`), driving sessions 2716 (`:282,294-308`), and instance state 2715 (`:171`), plus watermark and plan reads. These reads also apply to completion when the stated conditions hold.

Observed costs and limits:

- **Repeated active-contract lookup.** Completion looks up the active contract version again (2621+2623); dispatch runs the same lookup for some actions.
- **Per-row liveness in browse.** Browse runs two indexed statements per returned row (R6).
- **Cancelled-instance count.** Work-pin enrichment reads all 971 cancelled instances in the backup before filtering lifecycle and project; 2716 sorts driving sessions using a temporary B-tree (R14). No timing is claimed.
- **Expected sorts.** Q3 and Q7 use temporary B-trees for their declared ordering.
- **Q7 plan shapes not covered.** No exact plan was extracted for Q7 in DESC order without a cursor, or in ASC order with a cursor. Those two shapes are unverified.

## Evidence and method

- Repository: commit [`24eb4378804f1ce77378a842267dfdb72679e97d`](https://github.com/Sharper-Flow/concord/commit/24eb4378804f1ce77378a842267dfdb72679e97d). Paths and line numbers cite that commit.
- Native law context: registry `sha256:35c31da161478ebf167077da66e214a9330aa522a5ac48e43dd6671806a0dcce`, scanned commit `24eb4378804f1ce77378a842267dfdb72679e97d`; git fetch was run. Resolved artifacts: `workflow-engine-contract` `sha256:185b2ab7473fabc70448181fe88e245f924967383ff9158ab1f4cfab2ef9b7dc`; CD-0112 `sha256:80e0d8a0a56cee76c77ceb05e6c0ff733c9e43b549423c03e992a603a411c164`; `compaction-retention-policy` `sha256:64b1f021610dd828dad37257f8f0e90c613760f40df1b9c7422ff1ce521c8815`; PM10 `sha256:5ed9ac05406d425098f77d122e2db41e5f1727a05f652b0fff27c6a3b01e5549`; TS5 `sha256:19b8075db5b4802ccc524b2fa8bd9e1660eb4dd398fbf75eae446f7760bfd36d`.
- CD-0242 and CD-0224 were read from repository text at the subject commit; they were not in the native resolved artifact set above. Their native resolved status is not asserted.
- Store: the backup identified by SHA-256 above was opened with `mode=ro&immutable=1` for the production-driver replan and snapshot queries. The earlier authorizer harness used Python `sqlite3.connect('work.db')`, a read-write connection to a working copy; `idx.py` opened that copy read-only. The working-copy main file and backup both hash to `5f5a32b77e7482a012633b6c4ee1dac614699f4bed7fb298c7623b98fbbbdc2f`, with a zero-byte working-copy WAL at verification. This establishes current byte identity, not that the connection was immutable or that no write was possible. No writes are asserted. The live store was never queried.
- Diagnostic query classes:
  - `PRAGMA integrity_check`, `foreign_key_check`, `table_xinfo`, `index_list`/`index_xinfo` and `foreign_key_list`;
  - `dbstat` page sizes;
  - `SELECT count`/`GROUP BY` distributions;
  - CHECK violation counts as `WHERE NOT (expr)`;
  - `json_valid`/`json_type`/`json_each` profiles;
  - `EXPLAIN` and `EXPLAIN QUERY PLAN` with `foreign_keys(1)`;
  - preparing statements under an SQLite authorizer to attribute column reads and writes.
- Source diagnostics: static extraction of production SQL, a static call graph for reach, literal searches in non-test Go, and `git log -S` for emitter removal.
- Diagnostic scratch files are unpublished working files, inaccessible to Linear readers. They are not required to interpret the findings: the relevant results, plan lines, index sites and writer index appear in this document. Full raw extraction and per-day series are not published here.

### Evidence classes

- **Observed (query)**: a SELECT, `EXPLAIN` or `EXPLAIN QUERY PLAN` result from the snapshot. Production-driver replan and opcode counts come from modernc SQLite 3.54.1 (`modernc.org/sqlite v1.61.0`, pinned in `go.mod`); the earlier plans and authorizer attribution used Python SQLite 3.46.1 on the working copy. Byte sizes come from `dbstat` in SQLite 3.46.1.
- **Observed (source)**: text at the cited path and line at the subject commit.
- **Static extraction**: production SQL statements were extracted from non-test Go and prepared under an SQLite authorizer, which reports every table/column read and write. The extractor could not prepare 5 runtime SQL fragments; they were completed from source as 7 statements and prepared by hand (`internal/store/agent_mutation_boundary.go:153`, `internal/store/home_pair_binding.go:29`, `internal/store/membership.go:253` ×3, `internal/store/q7_current.go:117`, `internal/store/query.go:706`). Reach labels come from a static call graph: **runtime** means reachable from a non-store entry point without passing through migration functions. **Dormant fold** means one of 12 registered fold handlers (`internal/store/operation.go:140-188`) whose event kind is not constructed anywhere in non-test Go or TypeScript (found by a literal and constant search). **Upgrade** means reachable only through migrations.
- **Inference**: labelled *Inference* wherever it appears.

Size units: 1 MiB = 1,048,576 bytes.

## Store baseline (observed)

| Fact | Value |
|---|---|
| File size | 633,950,208 B (604.58 MiB) |
| Page size / pages / free pages | 4096 / 154,773 / 57,011 (233,517,056 B, 222.70 MiB free) |
| Schema objects | 63 user tables + `sqlite_sequence`; 461 + 2 columns; no views; 110 indexes (38 explicit, 72 automatic); 169 triggers; 201 CHECK clauses; 191 applied migrations |
| `sqlite_stat*` tables | none (the planner has no ANALYZE statistics) |
| Table + index pages, all user tables (`dbstat`) | 400,248,832 B (381.71 MiB) |
| Largest objects | `idempotency_records` 270,082,048 B (257.57 MiB); `effect_receipts` 47,951,872 B (45.73 MiB); `durable_operations` 36,925,440 B (35.21 MiB) |

## Appendix A — Growth and timestamp distributions

**Method.** For each table, every column named `*_at`, `*_time`, `observed_at` or `recorded_at` was profiled: non-null count, RFC 3339 shape (`GLOB 'YYYY-MM-DDT*'`), minimum and maximum. The growth column is the first non-null timestamp whose name does not denote a state change. Priority: `created_at`, `first_seen_at`, `recorded_at`, `observed_at`, `captured_at`, `occurred_at`, `approved_at`, `confirmed_at`. Names such as `updated_at`, `last_*`, `completed_at`, `revoked_at`, `started_at` and `expires_at` count as state changes. Rows are counted per UTC calendar day (`substr(col,1,10)`). Windows end on the newest timestamp in the store, 2026-11-07, which is a partial day.

**Limits.** (1) The name is taken to mean insertion time; the writers were not traced (*Inference*). (2) Only surviving rows are counted. Rows deleted by work removal, step disposal or migrations are invisible, so the series show the net surviving population, not the insert rate. (3) Bytes are current `dbstat` page bytes for the table plus its indexes, including unused space inside pages. Bytes per row is a mean, not a measured growth rate. No projection is given. The complete per-day series is not published; the table gives the totals and peak used here.

| Table | Rows | Table+index bytes | Growth column (coverage) | First–last day | Mean rows/day | Last 7 d | Last 30 d | Peak day | Mean B/row |
|---|---:|---:|---|---|---:|---:|---:|---|---:|
| `idempotency_records` | 47,270 | 270,082,048 | `first_observed_at` (47270/47270 rows) | 2026-09-01–2026-11-07 | 695.1 | 4,722 | 8,551 | 2026-09-30 (2,402) | 5,714 |
| `durable_operations` | 32,854 | 36,925,440 | `observed_at` (32854/32854 rows) | 2026-09-02–2026-11-07 | 490.4 | 4,168 | 6,362 | 2026-09-30 (1,922) | 1,124 |
| `workflow_actors` | 12,709 | 8,114,176 | `first_seen_at` (12709/12709 rows) | 2026-09-01–2026-11-07 | 186.9 | 199 | 767 | 2026-09-30 (876) | 638 |
| `work_items` | 2,378 | 4,210,688 | `created_at` (2378/2378 rows) | 2026-08-30–2026-11-07 | 34.0 | 225 | 390 | 2026-09-09 (131) | 1,771 |
| `workflow_design_records` | 641 | 2,940,928 | `recorded_at` (641/641 rows) | 2026-09-10–2026-11-04 | 11.4 | 56 | 160 | 2026-09-28 (40) | 4,588 |
| `workflow_overlap_resolutions` | 3,127 | 2,187,264 | `created_at` (3127/3127 rows) | 2026-09-03–2026-10-08 | 86.9 | 0 | 0 | 2026-09-09 (592) | 699 |
| `workflow_backlog_alignment` | 2,050 | 1,880,064 | `recorded_at` (2050/2050 rows) | 2026-09-20–2026-11-07 | 41.8 | 214 | 432 | 2026-09-30 (174) | 917 |
| `relations` | 2,783 | 1,740,800 | `created_at` (2783/2783 rows) | 2026-09-03–2026-10-22 | 55.7 | 0 | 1 | 2026-09-09 (494) | 626 |
| `workflow_proposal_records` | 506 | 1,339,392 | `recorded_at` (506/506 rows) | 2026-09-11–2026-11-04 | 9.2 | 55 | 131 | 2026-11-03 (36) | 2,647 |
| `workflow_contracts` | 2,090 | 737,280 | `approved_at` (2090/2090 rows) | 2026-09-03–2026-11-07 | 31.7 | 271 | 462 | 2026-09-29 (109) | 353 |
| `linear_issue_links` | 1,849 | 712,704 | `created_at` (1849/1849 rows) | 2026-09-09–2026-11-07 | 30.8 | 224 | 321 | 2026-09-28 (250) | 385 |
| `workflow_plan_records` | 121 | 712,704 | `recorded_at` (121/121 rows) | 2026-11-04–2026-11-07 | 30.2 | 121 | 121 | 2026-11-07 (47) | 5,890 |
| `workflow_delivery_state` | 671 | 524,288 | `occurred_at` (671/671 rows) | 2026-09-21–2026-11-07 | 14.0 | 212 | 275 | 2026-09-28 (53) | 781 |
| `domain_observations` | 316 | 253,952 | `recorded_at` (316/316 rows) | 2026-09-09–2026-10-03 | 12.6 | 0 | 0 | 2026-09-17 (28) | 804 |
| `workflow_premise_confirmations` | 1,150 | 225,280 | `confirmed_at` (1150/1150 rows) | 2026-09-04–2026-11-07 | 17.7 | 223 | 284 | 2026-09-29 (60) | 196 |
| `workflow_clarify_records` | 91 | 159,744 | `recorded_at` (87/91 rows) | 2026-11-06–2026-11-07 | 43.5 | 87 | 87 | 2026-11-07 (53) | 1,755 |
| `project_handoffs` | 24 | 106,496 | `recorded_at` (24/24 rows) | 2026-10-04–2026-11-01 | 0.8 | 1 | 9 | 2026-10-05 (10) | 4,437 |
| `external_observations` | 67 | 102,400 | `captured_at` (67/67 rows) | 2026-09-06–2026-10-08 | 2.0 | 0 | 0 | 2026-10-03 (16) | 1,528 |
| `workflow_removal_scopes` | 20 | 61,440 | `recorded_at` (20/20 rows) | 2026-11-04–2026-11-07 | 5.0 | 20 | 20 | 2026-11-06 (7) | 3,072 |
| `work_removal_operations` | 3 | 36,864 | `created_at` (3/3 rows) | 2026-09-26–2026-11-03 | 0.1 | 1 | 2 | 2026-09-26 (1) | 12,288 |
| `schema_migrations` | 191 | 32,768 | `applied_at` (191/191 rows) | 2026-08-08–2026-11-06 | 2.1 | 23 | 71 | 2026-08-30 (44) | 172 |
| `workflow_decision_records` | 5 | 32,768 | `recorded_at` (5/5 rows) | 2026-09-24–2026-10-01 | 0.6 | 0 | 0 | 2026-09-24 (2) | 6,554 |
| `project_locators` | 25 | 28,672 | `created_at` (25/25 rows) | 2026-08-30–2026-11-06 | 0.4 | 4 | 10 | 2026-08-30 (5) | 1,147 |
| `workflow_impact_edges` | 26 | 20,480 | `recorded_at` (26/26 rows) | 2026-09-04–2026-10-22 | 0.5 | 0 | 1 | 2026-09-08 (5) | 788 |
| `workflow_native_runs` | 4 | 20,480 | `recorded_at` (4/4 rows) | 2026-09-09–2026-09-11 | 1.3 | 0 | 0 | 2026-09-09 (2) | 5,120 |
| `agent_client_keys` | 8 | 16,384 | `created_at` (8/8 rows) | 2026-08-30–2026-09-16 | 0.4 | 0 | 0 | 2026-09-16 (5) | 2,048 |
| `agent_nonce_replay` | 3 | 12,288 | `observed_at` (3/3 rows) | 2026-10-30–2026-10-30 | 3.0 | 0 | 3 | 2026-10-30 (3) | 4,096 |
| `products` | 12 | 12,288 | `created_at` (12/12 rows) | 2026-08-30–2026-11-06 | 0.2 | 2 | 3 | 2026-08-30 (3) | 1,024 |
| `agent_clients` | 3 | 8,192 | `created_at` (3/3 rows) | 2026-08-30–2026-09-02 | 0.8 | 0 | 0 | 2026-08-30 (2) | 2,731 |
| `agent_installation_keys` | 1 | 8,192 | `created_at` (1/1 rows) | 2026-08-08–2026-08-08 | 1.0 | 0 | 0 | 2026-08-08 (1) | 8,192 |
| `projects` | 16 | 8,192 | `created_at` (16/16 rows) | 2026-08-30–2026-11-06 | 0.2 | 2 | 3 | 2026-08-30 (5) | 512 |

**Not measurable** (no usable insertion timestamp):

| Table | Rows | Table+index bytes | Reason |
|---|---:|---:|---|
| `effect_receipts` | 108,765 | 47,951,872 | no timestamp column |
| `workflow_evidence_bindings` | 10,305 | 8,814,592 | no timestamp column |
| `work_version_actions` | 33,494 | 5,447,680 | no timestamp column |
| `workflow_instances` | 2,376 | 2,125,824 | only state-change timestamps (started_at,completed_at,last_checkpoint_at,execution_started_at,completion_occurred_at) |
| `workflow_contract_affected_domains` | 3,998 | 733,184 | no timestamp column |
| `work_projects` | 2,465 | 536,576 | no timestamp column |
| `workflow_architecture_bindings` | 1,942 | 475,136 | no timestamp column |
| `archived_work` | 415 | 323,584 | only state-change timestamps (completed_at) |
| `workflow_contract_law_modifications` | 1,287 | 217,088 | no timestamp column |
| `workflow_contract_domain_modifications` | 764 | 102,400 | no timestamp column |
| `workflow_law_addition_reservations` | 86 | 32,768 | no timestamp column |
| `workflow_contract_law_additions` | 116 | 28,672 | no timestamp column |
| `workflow_contract_verification_obligations` | 75 | 24,576 | no timestamp column |
| `product_projects` | 16 | 16,384 | no timestamp column |
| `workflow_checkpoints` | 0 | 16,384 | timestamp columns empty |
| `archived_work_domains` | 27 | 12,288 | no timestamp column |
| `archived_work_products` | 12 | 12,288 | no timestamp column |
| `archived_work_projects` | 12 | 12,288 | no timestamp column |
| `archived_work_tags` | 6 | 12,288 | no timestamp column |
| `product_knowledge_homes` | 12 | 12,288 | no timestamp column |
| `runtime_state_writers` | 5 | 8,192 | no timestamp column |
| `store_sequences` | 2 | 8,192 | no timestamp column |
| `work_kinds` | 7 | 8,192 | no timestamp column |
| `workflow_completed_exits` | 16 | 8,192 | no timestamp column |
| `workflow_contract_domain_relation_modifications` | 18 | 8,192 | no timestamp column |
| `workflow_coordinator_actions` | 5 | 8,192 | only state-change timestamps (last_acted_at) |
| `workflow_native_run_statuses` | 10 | 8,192 | no timestamp column |
| `workflow_phase_exits` | 16 | 8,192 | no timestamp column |
| `workflow_step_epochs` | 3 | 8,192 | no timestamp column |
| `durability_commits` | 1 | 4,096 | no timestamp column |
| `store_revision` | 1 | 4,096 | no timestamp column |
| `write_guard` | 0 | 4,096 | no timestamp column |

`effect_receipts` (108,765 rows) has no timestamp column; its growth can only be stated through its sequence, not time.

## Appendix B — JSON columns and their uses

**Method.** A TEXT or BLOB column counts as JSON when either (a) a CHECK calls `json_valid` on it, or (b) every non-null value is valid JSON and at least one is an object or array, or (c) its name ends in `_json`. **SQL uses** were found by scanning extracted statements and schema objects for `json_extract`, `->`/`->>`, `json_each`/`json_tree` and `json_type`/`json_array_length`/`json_valid`. Each argument was resolved to a column through the statement's authorizer reads. The clause position comes from the nearest preceding SQL keyword (a textual rule). **Go parsing**: the enclosing Go function of each runtime read site was checked for a JSON decode call (`json.Unmarshal`, `json.NewDecoder`, `.Decode(`). Callers were not traced, so "no decode call" does not mean "never parsed". Validation-only CHECK uses are listed in Appendix C, not here.

Runtime SQL field queries (observed):

- `workflow_step_epochs.latest_start_payload`: `json_extract(...,'$.actor_ref')` in a `LEFT JOIN ... ON` predicate, at `internal/store/project_handoffs.go:986` (`workExecutionStoppedTx`).
- `durable_operations.evidence_refs`: `EXISTS (SELECT 1 FROM json_each(durable_operations.evidence_refs) WHERE value=?)`, at `internal/store/workflow.go:1425` (`foldWorkflowEvidenceBound`). This matches an array element, not a field.
- `workflow_evidence_bindings.payload`: (i) the expression index `workflow_evidence_bindings_subject(work_id, json_extract(payload,'$.immutable_subject_ref'))`; (ii) `json_extract` in the projection of `currentQ7EntriesSQL`, `internal/store/q7_current.go:193-214`, which is output shaping, not a filter. No runtime statement filters on `json_extract(payload,'$.immutable_subject_ref')`. Plans that use this index match only its `work_id` prefix.
- No other JSON column is queried by field in runtime SQL. `work_items.intent_json`, `workflow_instances.prelude_state` and `workflow_contracts.law_modifies` have field access only in upgrade/migration SQL.

| Column | Non-null | JSON types | CHECK-declared | Value bytes | Class | Runtime read sites | …with decode in function | Non-runtime SQL uses |
|---|---:|---|---|---:|---|---:|---:|---|
| `idempotency_records.result_payload` | 47,270 | object=47,270 | no | 195,110,211 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 2 | 1 | — |
| `idempotency_records.authorized_scope_snapshot` | 47,270 | object=47,270 | no | 18,554,801 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 1 | 0 | — |
| `durable_operations.result_payload` | 32,854 | object=32,854 | no | 6,329,331 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 1 | 1 | — |
| `durable_operations.accepted_scope_snapshot` | 32,854 | object=32,854 | no | 6,294,708 | no runtime reader | 0 | 0 | — |
| `workflow_evidence_bindings.payload` | 10,305 | object=10,305 | yes | 4,348,188 | field projected in runtime SQL (not filtered) | 1 | 1 | SCHEMA:json_extract/schema; UP:json_extract/predicate |
| `idempotency_records.changed_refs` | 47,270 | array=47,210, null=60 | no | 4,111,526 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 2 | 1 | — |
| `durable_operations.changed_refs` | 32,854 | array=32,854 | no | 2,989,593 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 1 | 1 | — |
| `durable_operations.evidence_refs` | 32,854 | array=32,854 | no | 2,224,331 | queried-by-element in runtime SQL (json_each in predicate subquery) | 2 | 1 | — |
| `work_items.intent_json` | 2,378 | object=2,378 | no | 2,102,814 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 2 | 0 | UP:json_extract/predicate; UP:json_type/predicate |
| `workflow_design_records.decisions` | 641 | array=641 | yes | 1,376,490 | no runtime reader | 0 | 0 | — |
| `workflow_instances.step_plan` | 148 | object=148 | yes | 606,904 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 3 | 0 | — |
| `workflow_plan_records.payload` | 121 | object=121 | yes | 571,904 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 1 | 0 | — |
| `workflow_instances.completion_payload` | 1,104 | object=1,104 | yes | 434,201 | no runtime reader | 0 | 0 | — |
| `workflow_delivery_state.payload` | 671 | object=671 | yes | 328,511 | no runtime reader | 0 | 0 | — |
| `workflow_proposal_records.user_outcomes` | 506 | array=506 | yes | 225,194 | no runtime reader | 0 | 0 | — |
| `workflow_design_records.touched_refs` | 641 | array=641 | yes | 199,055 | no runtime reader | 0 | 0 | — |
| `workflow_contracts.legislative_subjects` | 2,090 | array=2,090 | yes | 192,810 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 2 | 2 | — |
| `work_items.transition_evidence_refs` | 2,378 | array=2,378 | yes | 140,998 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 1 | 1 | — |
| `workflow_proposal_records.affected` | 506 | array=506 | yes | 117,459 | no runtime reader | 0 | 0 | — |
| `workflow_clarify_records.payload` | 91 | object=91 | yes | 104,364 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 1 | 0 | — |
| `workflow_proposal_records.constraints` | 506 | array=506 | yes | 59,172 | no runtime reader | 0 | 0 | — |
| `external_observations.observed_universe` | 67 | object=67 | no | 40,969 | no runtime reader | 0 | 0 | — |
| `workflow_instances.prelude_state` | 148 | object=148 | yes | 40,376 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 4 | 0 | UP:json_extract/predicate; UP:json_extract/projection; UP:json_type/predicate |
| `workflow_removal_scopes.payload` | 20 | object=20 | yes | 37,196 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 1 | 0 | — |
| `domain_observations.refs` | 316 | array=316 | yes | 31,764 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 1 | 1 | — |
| `workflow_contracts.spec_mandate` | 2,090 | array=2,090 | yes | 26,297 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 3 | 3 | — |
| `workflow_proposal_records.out_of_scope` | 506 | array=506 | yes | 21,036 | no runtime reader | 0 | 0 | — |
| `workflow_contracts.law_modifies` | 2,090 | array=2,090 | yes | 18,211 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 4 | 3 | UP:json_each/table-valued; UP:json_type/predicate; UP:json_valid/predicate |
| `archived_work.lesson_tags` | 415 | array=415 | no | 14,450 | no runtime reader | 0 | 0 | — |
| `workflow_proposal_records.open_questions` | 506 | array=506 | yes | 13,145 | no runtime reader | 0 | 0 | — |
| `project_handoffs.changes` | 24 | array=23, null=1 | yes | 11,177 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 1 | 1 | — |
| `work_removal_operations.handoff_json` | 3 | object=3 | yes | 11,102 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 1 | 1 | — |
| `project_handoffs.verification` | 24 | array=24 | yes | 10,854 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 1 | 1 | — |
| `workflow_contracts.self_repair_json` | 2,090 | null=2,085, object=5 | yes | 9,823 | no runtime reader | 0 | 0 | — |
| `domain_observations.tags` | 316 | array=316 | yes | 9,295 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 1 | 1 | — |
| `project_handoffs.artifact_refs` | 24 | array=4, null=20 | yes | 4,450 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 2 | 2 | — |
| `workflow_decision_records.consequences` | 5 | array=5 | yes | 4,253 | no runtime reader | 0 | 0 | — |
| `project_handoffs.blockers` | 24 | array=17, null=7 | yes | 3,780 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 1 | 1 | — |
| `workflow_decision_records.inputs` | 5 | array=5 | yes | 2,282 | no runtime reader | 0 | 0 | — |
| `workflow_native_runs.observed_universe` | 4 | object=4 | yes | 1,888 | no runtime reader | 0 | 0 | — |
| `workflow_decision_records.options_considered` | 5 | array=5 | yes | 1,386 | no runtime reader | 0 | 0 | — |
| `workflow_step_epochs.latest_start_payload` | 3 | object=3 | yes | 1,181 | queried-by-field in runtime SQL (predicate) | 2 | 1 | — |
| `work_removal_operations.linear_confirmation_json` | 3 | object=3 | yes | 618 | parsed in Go: reading function contains a JSON decode call; no runtime SQL field access | 2 | 1 | — |
| `external_observations.verification_omissions` | 60 | array=1, null=59 | no | 346 | no runtime reader | 0 | 0 | — |
| `agent_clients.project_scope_json` | 3 | array=3 | yes | 277 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 2 | 0 | — |
| `agent_clients.capabilities_json` | 3 | array=3 | yes | 249 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 2 | 0 | — |
| `agent_clients.agent_scope_json` | 3 | array=3 | yes | 217 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 2 | 0 | — |
| `agent_clients.product_scope_json` | 3 | array=3 | yes | 189 | read by runtime SQL; no SQL field access; reading function has no decode call (callers not traced) | 2 | 0 | — |
| `workflow_checkpoints.result_evidence_refs` | 0 |  | yes | 0 | no runtime reader | 0 | 0 | — |

The table gives read-site counts; the SQL field uses relevant to the findings are listed above.

## Appendix C — CHECK constraints (201)

**Method.** All CHECK clauses were parsed from the snapshot's `sqlite_schema` table definitions with balanced parentheses: 201 clauses. For each one, `SELECT count(*) FROM t WHERE NOT (expr)` gives current violations: **0 in total**. For vocabulary and boolean CHECKs, every listed value was counted with `GROUP BY`. Writers are the extracted statements that write the column (RT runtime, DF dormant fold, UP upgrade). "Go literal sites" counts quoted occurrences of the value in non-test Go, excluding `internal/store/schema.go`. A literal is not proof of a write. A bound-parameter writer can write a value that never appears literally.

Kinds: length/range/type 77, json-shape 49, vocabulary 38, cross-column 15, boolean 8, format/prefix 7, fixed-constant 7.

### Vocabulary and boolean CHECKs

| Column | Values = rows | NULL rows | Zero-row values | Writers (class: count) | Zero-row value evidence |
|---|---|---:|---|---|---|
| `agent_client_keys.status` | `active`=3, `revoked`=5 | 0 | — | RT: 4 | — |
| `agent_clients.status` | `active`=3, `revoked`=0 | 0 | `revoked` | RT: 2 | `revoked`: stmt literal `internal/store/agent_authority.go:148`, Go literal sites 3 |
| `archived_work.terminal_state` | `completed`=415, `cancelled`=0, `superseded`=0 | 0 | `cancelled`, `superseded` | none extracted | `cancelled`: stmt literal none, Go literal sites 43; `superseded`: stmt literal none, Go literal sites 3 |
| `archived_work.scope_mode` | `home`=402, `explicit`=13 | 0 | — | none extracted | — |
| `domain_observations.state` | `open`=303, `dismissed`=13 | 0 | — | DF: 2 | — |
| `durability_commits.bit` | `0`=1, `1`=0 | 0 | `1` | RT: 1, UP: 1 | `1`: stmt literal none, Go literal sites n/a |
| `external_observations.capture_method` | `trusted_client_report`=67, `git_probe`=0 | 0 | `git_probe` | DF: 2 | `git_probe`: stmt literal none, Go literal sites 2 |
| `external_observations.verification_state` | `unverified`=7, `verified`=60, `diverged_expected`=0, `diverged_unexpected`=0, `unverifiable`=0 | 0 | `diverged_expected`, `diverged_unexpected`, `unverifiable` | DF: 3 | `diverged_expected`: stmt literal none, Go literal sites 1; `diverged_unexpected`: stmt literal none, Go literal sites 1; `unverifiable`: stmt literal none, Go literal sites 1 |
| `product_projects.role` | `primary`=12, `secondary`=4 | 0 | — | RT: 3 | — |
| `products.stage_maturity` | `prototype`=3, `alpha`=0, `beta`=1, `production`=8, `deprecated`=0 | 0 | `alpha`, `deprecated` | RT: 2 | `alpha`: stmt literal none, Go literal sites 1; `deprecated`: stmt literal none, Go literal sites 2 |
| `products.stage_audience_commitment` | `operator_only`=7, `limited`=0, `public`=5 | 0 | `limited` | RT: 2 | `limited`: stmt literal none, Go literal sites 1 |
| `project_handoffs.state` | `recorded`=2, `consumed`=22 | 0 | — | RT: 2 | — |
| `project_locators.kind` | `git_remote`=9, `canonical_path`=16 | 0 | — | RT: 2 | — |
| `project_locators.normalization_version` | `1`=13, `2`=12 | 0 | — | RT: 2 | — |
| `projects.stage_maturity_override` | `prototype`=0, `alpha`=0, `beta`=0, `production`=0, `deprecated`=0 | 16 | `prototype`, `alpha`, `beta`, `production`, `deprecated` | DF: 1, RT: 1 | `prototype`: stmt literal none, Go literal sites 4; `alpha`: stmt literal none, Go literal sites 1; `beta`: stmt literal none, Go literal sites 1; `production`: stmt literal none, Go literal sites 1; `deprecated`: stmt literal none, Go literal sites 2 |
| `projects.stage_audience_commitment_override` | `operator_only`=0, `limited`=0, `public`=0 | 16 | `operator_only`, `limited`, `public` | DF: 1, RT: 1 | `operator_only`: stmt literal none, Go literal sites 4; `limited`: stmt literal none, Go literal sites 1; `public`: stmt literal none, Go literal sites 1 |
| `projects.repository_mode` | `on`=7, `off`=9 | 0 | — | RT: 1 | — |
| `relations.kind` | `parent`=0, `blocks`=91, `implements`=12, `forward_link`=18, `raised_from`=277, `depends_on`=126, `compatible_with`=2,259 | 0 | `parent` | RT: 1 | `parent`: stmt literal none, Go literal sites 10 |
| `runtime_state_writers.surface` | `knowledge`=5, `workflow`=0 | 0 | `workflow` | none extracted | `workflow`: stmt literal none, Go literal sites 0 |
| `store_sequences.name` | `effect`=1, `relation`=1 | 0 | — | none extracted | — |
| `work_items.lifecycle` | `needed`=17, `in_progress`=21, `completed`=1,368, `cancelled`=972 | 0 | — | RT: 3, UP: 1 | — |
| `work_kinds.stored` | `0`=2, `1`=5 | 0 | — | UP: 2 | — |
| `work_kinds.fold_create` | `0`=2, `1`=5 | 0 | — | UP: 2 | — |
| `work_kinds.fold_revise` | `0`=2, `1`=5 | 0 | — | UP: 1 | — |
| `work_kinds.agent_capture` | `0`=2, `1`=5 | 0 | — | UP: 1 | — |
| `work_projects.role` | `primary`=2,378, `secondary`=87 | 0 | — | RT: 4 | — |
| `work_removal_operations.reason` | `shelved`=2, `cancelled`=1 | 0 | — | RT: 2 | — |
| `work_removal_operations.state` | `prepared`=0, `committed`=3 | 0 | `prepared` | RT: 3 | `prepared`: stmt literal `internal/store/work_removal.go:210`, Go literal sites 3 |
| `workflow_actors.actor_class` | `agent`=5,326, `operator`=7,383 | 0 | — | RT: 1 | — |
| `workflow_backlog_alignment.outcome` | `related_found`=1,776, `none_found`=274 | 0 | — | RT: 2 | — |
| `workflow_checkpoints.step_kind` | `internal_sqlite`=0, `cross_authority`=0, `external_effect`=0 | 0 | `internal_sqlite`, `cross_authority`, `external_effect` | RT: 1 | `internal_sqlite`: stmt literal none, Go literal sites 7; `cross_authority`: stmt literal none, Go literal sites 3; `external_effect`: stmt literal none, Go literal sites 3 |
| `workflow_contract_domain_relation_modifications.kind` | `depends_on`=14, `shares_contract_with`=4, `replaces`=0 | 0 | `replaces` | RT: 1 | `replaces`: stmt literal none, Go literal sites 4 |
| `workflow_contracts.consequence_class` | `internal_sqlite`=2,090, `cross_authority`=0, `external_effect`=0 | 0 | `cross_authority`, `external_effect` | RT: 1 | `cross_authority`: stmt literal none, Go literal sites 3; `external_effect`: stmt literal none, Go literal sites 3 |
| `workflow_contracts.law_boundary_version` | `0`=0, `1`=2,090 | 0 | `0` | RT: 1 | `0`: stmt literal none, Go literal sites n/a |
| `workflow_contracts.changes_product_truth` | `0`=6, `1`=154 | 1,930 | — | RT: 1 | — |
| `workflow_decision_records.decision` | `accepted_decision`=5, `insufficient_evidence`=0 | 0 | `insufficient_evidence` | RT: 1 | `insufficient_evidence`: stmt literal none, Go literal sites 4 |
| `workflow_impact_edges.edge_kind` | `modifies`=8, `depends_on`=0, `forward_link`=18 | 0 | `depends_on` | RT: 1 | `depends_on`: stmt literal none, Go literal sites 19 |
| `workflow_impact_edges.edge_class` | `hard`=25, `soft`=0, `none`=1 | 0 | `soft` | RT: 1 | `soft`: stmt literal none, Go literal sites 0 |
| `workflow_impact_edges.target_kind` | `work_item`=26 | 0 | — | RT: 1 | — |
| `workflow_impact_edges.severity` | `breaking`=0, `non-breaking`=8, `informational`=18 | 0 | `breaking` | RT: 1 | `breaking`: stmt literal none, Go literal sites 4 |
| `workflow_instances.workflow_type` | `change`=67, `fix`=54, `research`=7, `removal`=20 | 2,228 | — | RT: 1 | — |
| `workflow_instances.instance_state` | `intake`=32, `running`=5, `blocked`=0, `verifying`=0, `completed`=1,368, `cancelled`=971 | 0 | `blocked`, `verifying` | RT: 8, UP: 1 | `blocked`: stmt literal none, Go literal sites 4; `verifying`: stmt literal none, Go literal sites 0 |
| `workflow_native_run_statuses.failure` | `0`=4, `1`=6 | 0 | — | UP: 1 | — |
| `workflow_native_runs.phase` | `start`=2, `health`=2, `rollback`=0, `cleanup`=0 | 0 | `rollback`, `cleanup` | DF: 1, UP: 1 | `rollback`: stmt literal none, Go literal sites 1; `cleanup`: stmt literal none, Go literal sites 1 |
| `workflow_native_runs.verification_state` | `unverified`=3, `verified`=1, `diverged_expected`=0, `diverged_unexpected`=0, `unverifiable`=0 | 0 | `diverged_expected`, `diverged_unexpected`, `unverifiable` | DF: 1 | `diverged_expected`: stmt literal none, Go literal sites 1; `diverged_unexpected`: stmt literal none, Go literal sites 1; `unverifiable`: stmt literal none, Go literal sites 1 |
| `workflow_overlap_resolutions.resolution_kind` | `compatible_with`=2,984, `depends_on`=77, `blocks`=66 | 0 | — | RT: 1 | — |

### Terms checked by name

- **`verifying`** (`workflow_instances.instance_state`, 0 rows). Law declares it: `.concord/law/specs/workflow-engine-contract.md:191-203` (closed state list, transitions `running`→`verifying` and `verifying`→`completed`). The payload schema lists it: `contracts/agent-tool-surface-payloads.schema.json:1848`. Observed: no non-test Go file outside `internal/store/schema.go` contains the literal `verifying` as a state. The extracted writers of `instance_state` write `'running'` literally (`internal/store/workflow.go:1018`, `internal/store/workflow_step.go:30`), `blocked`/`running` from a variable (`internal/store/workflow.go:1364-1368`), `cancelled` (`internal/store/lifecycle.go:632-633`), or the terminal state of a completion payload (`internal/store/workflow.go:1622`). Result: the law-declared `running`→`verifying` transition has no writer in this tree. This is a law/implementation gap, not a removal candidate.
- **`soft`** (`workflow_impact_edges.edge_class`, 0 rows; `none` = 1 row, `hard` = 25). The payload schema offers `hard|soft|none`: `contracts/agent-tool-surface-payloads.schema.json:3633-3640`. The table is declared in `.concord/law/specs/workflow-engine-contract.md:397`. The only extracted writer, `internal/store/workflow.go:1533` (`foldWorkflowSuccessorLinked`), always binds `edge_kind='forward_link'` and `edge_class='hard'`. The runtime readers filter `edge_class='hard'` (`internal/store/workflow.go:1548,1551`). *Inference*: the 8 `modifies` rows and the 1 `none` row were written by earlier code.
- **`superseded`**: (i) `archived_work.terminal_state` allows `completed|cancelled|superseded` (415/0/0 rows). There is no extracted runtime writer; the table is retained by `.concord/law/specs/compaction-retention-policy.md` §2 (field list at lines 57-63). (ii) Current `work_items.lifecycle` has no `superseded` value; the older vocabulary survives in non-test Go at `internal/store/initiative_retirement.go:17` and `internal/store/verification_retirement.go:39`, and in migration text in `internal/store/schema.go`. `workflow_instances.instance_state` (current CHECK) also lacks it.
- **`runtime_state_writers`** (5 rows, all `surface='knowledge'`). Its CHECK still allows `workflow`. Observed: in non-test Go the table name appears only in `internal/store/schema.go`: creation at line 5682, rebuild at 7544-7558, and `DELETE FROM runtime_state_writers WHERE surface='workflow'` at 8746. There is no runtime reader or writer; the snapshot digest reads the table like every other table.

### All CHECK clauses

| # | Table | Kind | Columns | Violations | Expression |
|---:|---|---|---|---:|---|
| 1 | `agent_client_keys` | vocabulary | status | 0 | `status IN ('active','revoked')` |
| 2 | `agent_client_keys` | length/range/type | key_id | 0 | `length(key_id) > 0 AND length(key_id) <= 128` |
| 3 | `agent_client_keys` | length/range/type | public_key | 0 | `length(public_key) = 32` |
| 4 | `agent_client_keys` | cross-column | revoked_at, status | 0 | `(status='active' AND revoked_at IS NULL) OR (status='revoked' AND revoked_at IS NOT NULL)` |
| 5 | `agent_clients` | vocabulary | status | 0 | `status IN ('active','revoked')` |
| 6 | `agent_clients` | json-shape | capabilities_json | 0 | `json_valid(capabilities_json) AND json_type(capabilities_json)='array' AND json_array_length(capabilities_json) <= 32` |
| 7 | `agent_clients` | json-shape | product_scope_json | 0 | `json_valid(product_scope_json) AND json_type(product_scope_json)='array' AND json_array_length(product_scope_json) <= 100` |
| 8 | `agent_clients` | json-shape | project_scope_json | 0 | `json_valid(project_scope_json) AND json_type(project_scope_json)='array' AND json_array_length(project_scope_json) <= 100` |
| 9 | `agent_clients` | json-shape | agent_scope_json | 0 | `json_valid(agent_scope_json) AND json_type(agent_scope_json)='array' AND json_array_length(agent_scope_json) <= 100` |
| 10 | `agent_clients` | length/range/type | client_ref | 0 | `length(client_ref) > 0 AND length(client_ref) <= 128` |
| 11 | `agent_clients` | length/range/type | principal_ref | 0 | `length(principal_ref) > 0 AND length(principal_ref) <= 128` |
| 12 | `agent_clients` | cross-column | revoked_at, status | 0 | `(status='active' AND revoked_at IS NULL) OR (status='revoked' AND revoked_at IS NOT NULL)` |
| 13 | `agent_installation_keys` | length/range/type | key_bytes | 0 | `length(key_bytes) = 32` |
| 14 | `agent_nonce_replay` | length/range/type | nonce | 0 | `length(nonce) >= 16 AND length(nonce) <= 256` |
| 15 | `agent_nonce_replay` | cross-column | expires_at, observed_at | 0 | `length(observed_at) > 0 AND length(expires_at) > 0` |
| 16 | `archived_work` | vocabulary | terminal_state | 0 | `terminal_state IN ('completed','cancelled','superseded')` |
| 17 | `archived_work` | vocabulary | scope_mode | 0 | `scope_mode IN ('home','explicit')` |
| 18 | `archived_work_domains` | length/range/type | domain_id | 0 | `length(domain_id) BETWEEN 1 AND 256` |
| 19 | `domain_observations` | format/prefix | observation_id | 0 | `length(observation_id) = 20 AND substr(observation_id,1,4) = 'dob:'` |
| 20 | `domain_observations` | length/range/type | statement | 0 | `length(statement) > 0 AND length(statement) <= 512` |
| 21 | `domain_observations` | json-shape | refs | 0 | `json_valid(refs) AND json_type(refs)='array' AND json_array_length(refs) <= 16` |
| 22 | `domain_observations` | json-shape | tags | 0 | `json_valid(tags) AND json_type(tags)='array' AND json_array_length(tags) <= 8` |
| 23 | `domain_observations` | vocabulary | state | 0 | `state IN ('open','dismissed')` |
| 24 | `domain_observations` | cross-column | dismissed_at, state | 0 | `(state = 'dismissed') = (dismissed_at IS NOT NULL)` |
| 25 | `durability_commits` | fixed-constant | id | 0 | `id = 1` |
| 26 | `durability_commits` | boolean | bit | 0 | `bit IN (0,1)` |
| 27 | `durable_operations` | fixed-constant | attempt_epoch | 0 | `attempt_epoch=1` |
| 28 | `durable_operations` | fixed-constant | step_kind | 0 | `step_kind='internal_sqlite'` |
| 29 | `durable_operations` | fixed-constant | result_kind | 0 | `result_kind='completed'` |
| 30 | `external_observations` | format/prefix | observation_id | 0 | `observation_id LIKE 'xobs:%'` |
| 31 | `external_observations` | length/range/type | subject_kind | 0 | `length(subject_kind) BETWEEN 1 AND 64` |
| 32 | `external_observations` | length/range/type | subject_ref | 0 | `length(subject_ref) BETWEEN 1 AND 2048` |
| 33 | `external_observations` | vocabulary | capture_method | 0 | `capture_method IN ('trusted_client_report','git_probe')` |
| 34 | `external_observations` | length/range/type | reporting_authority_ref | 0 | `length(reporting_authority_ref) BETWEEN 1 AND 256` |
| 35 | `external_observations` | format/prefix | subject_digest | 0 | `subject_digest IS NULL OR subject_digest LIKE 'sha256:%'` |
| 36 | `external_observations` | vocabulary | verification_state | 0 | `verification_state IN ('unverified','verified','diverged_expected','diverged_unexpected','unverifiable')` |
| 37 | `linear_issue_links` | length/range/type | work_id | 0 | `length(work_id) BETWEEN 2 AND 128` |
| 38 | `linear_issue_links` | length/range/type | remote_issue_uuid | 0 | `length(remote_issue_uuid) BETWEEN 2 AND 128` |
| 39 | `linear_issue_links` | length/range/type | human_key | 0 | `length(human_key) BETWEEN 1 AND 64` |
| 40 | `linear_issue_links` | length/range/type | url | 0 | `length(url) BETWEEN 1 AND 2048` |
| 41 | `product_projects` | vocabulary | role | 0 | `role IN ('primary','secondary')` |
| 42 | `products` | vocabulary | stage_maturity | 0 | `stage_maturity IN ('prototype', 'alpha', 'beta', 'production', 'deprecated')` |
| 43 | `products` | vocabulary | stage_audience_commitment | 0 | `stage_audience_commitment IN ('operator_only', 'limited', 'public')` |
| 44 | `project_handoffs` | length/range/type | contract_version | 0 | `contract_version > 0` |
| 45 | `project_handoffs` | json-shape | changes | 0 | `json_valid(changes)` |
| 46 | `project_handoffs` | json-shape | verification | 0 | `json_valid(verification)` |
| 47 | `project_handoffs` | json-shape | artifact_refs | 0 | `json_valid(artifact_refs)` |
| 48 | `project_handoffs` | json-shape | blockers | 0 | `json_valid(blockers)` |
| 49 | `project_handoffs` | vocabulary | state | 0 | `state IN ('recorded','consumed')` |
| 50 | `project_handoffs` | cross-column | source_project_id, target_project_id | 0 | `source_project_id <> target_project_id` |
| 51 | `project_locators` | vocabulary | kind | 0 | `kind IN ('git_remote','canonical_path')` |
| 52 | `project_locators` | vocabulary | normalization_version | 0 | `normalization_version IN (1,2)` |
| 53 | `project_locators` | length/range/type | locator_id | 0 | `length(locator_id) > 0 AND length(locator_id) <= 128` |
| 54 | `project_locators` | length/range/type | locator_value | 0 | `length(locator_value) > 0 AND length(locator_value) <= 4096` |
| 55 | `project_locators` | length/range/type | normalized_value | 0 | `length(normalized_value) > 0 AND length(normalized_value) <= 4096` |
| 56 | `projects` | vocabulary | stage_maturity_override | 0 | `stage_maturity_override IS NULL OR stage_maturity_override IN ('prototype','alpha','beta','production','deprecated')` |
| 57 | `projects` | vocabulary | stage_audience_commitment_override | 0 | `stage_audience_commitment_override IS NULL OR stage_audience_commitment_override IN ('operator_only','limited','public')` |
| 58 | `projects` | vocabulary | repository_mode | 0 | `repository_mode IN ('on','off')` |
| 59 | `relations` | vocabulary | kind | 0 | `kind IN ('parent','blocks','implements','forward_link','raised_from','depends_on','compatible_with')` |
| 60 | `relations` | cross-column | work_id_from, work_id_to | 0 | `work_id_from<>work_id_to` |
| 61 | `runtime_state_writers` | vocabulary | surface | 0 | `surface IN ('knowledge','workflow')` |
| 62 | `runtime_state_writers` | cross-column | knowledge_projection_version, surface | 0 | `(surface='knowledge' AND knowledge_projection_version > 0) OR (surface='workflow' AND knowledge_projection_version=0)` |
| 63 | `store_revision` | fixed-constant | id | 0 | `id=1` |
| 64 | `store_revision` | length/range/type | revision | 0 | `typeof(revision)='integer' AND revision>=0` |
| 65 | `store_sequences` | vocabulary | name | 0 | `name IN ('effect','relation')` |
| 66 | `store_sequences` | length/range/type | last | 0 | `last>=0` |
| 67 | `work_items` | vocabulary | lifecycle | 0 | `lifecycle IN ('needed','in_progress','completed','cancelled')` |
| 68 | `work_items` | length/range/type | narrative | 0 | `length(narrative)<=16384` |
| 69 | `work_items` | length/range/type | effect_seq | 0 | `typeof(effect_seq)='integer' AND effect_seq>=0` |
| 70 | `work_items` | json-shape | transition_evidence_refs | 0 | `json_valid(transition_evidence_refs) AND json_type(transition_evidence_refs)='array'` |
| 71 | `work_kinds` | boolean | stored | 0 | `stored IN (0,1)` |
| 72 | `work_kinds` | boolean | fold_create | 0 | `fold_create IN (0,1)` |
| 73 | `work_kinds` | boolean | fold_revise | 0 | `fold_revise IN (0,1)` |
| 74 | `work_kinds` | boolean | agent_capture | 0 | `agent_capture IN (0,1)` |
| 75 | `work_projects` | vocabulary | role | 0 | `role IN ('primary','secondary')` |
| 76 | `work_removal_operations` | length/range/type | operation_id | 0 | `length(operation_id) BETWEEN 2 AND 128` |
| 77 | `work_removal_operations` | length/range/type | idempotency_key | 0 | `length(idempotency_key) BETWEEN 2 AND 128` |
| 78 | `work_removal_operations` | length/range/type | work_id | 0 | `length(work_id) BETWEEN 2 AND 128` |
| 79 | `work_removal_operations` | length/range/type | expected_version | 0 | `expected_version > 0` |
| 80 | `work_removal_operations` | vocabulary | reason | 0 | `reason IN ('shelved','cancelled')` |
| 81 | `work_removal_operations` | length/range/type | actor | 0 | `length(actor) BETWEEN 2 AND 128` |
| 82 | `work_removal_operations` | json-shape | handoff_json | 0 | `json_valid(handoff_json) AND json_type(handoff_json)='object'` |
| 83 | `work_removal_operations` | format/prefix | handoff_digest | 0 | `length(handoff_digest)=71 AND substr(handoff_digest,1,7)='sha256:'` |
| 84 | `work_removal_operations` | vocabulary | state | 0 | `state IN ('prepared','committed')` |
| 85 | `work_removal_operations` | json-shape | linear_confirmation_json | 0 | `json_valid(linear_confirmation_json) AND json_type(linear_confirmation_json)='object'` |
| 86 | `workflow_actors` | vocabulary | actor_class | 0 | `actor_class IN ('agent','operator')` |
| 87 | `workflow_actors` | format/prefix | actor_ref | 0 | `length(actor_ref) = 70 AND substr(actor_ref,1,6) = 'actor:'` |
| 88 | `workflow_actors` | length/range/type | principal_ref | 0 | `length(principal_ref) BETWEEN 2 AND 128` |
| 89 | `workflow_actors` | length/range/type | client_ref | 0 | `length(client_ref) BETWEEN 2 AND 128` |
| 90 | `workflow_actors` | length/range/type | agent_ref | 0 | `length(agent_ref) BETWEEN 2 AND 128` |
| 91 | `workflow_actors` | length/range/type | session_ref | 0 | `length(session_ref) BETWEEN 2 AND 128` |
| 92 | `workflow_actors` | length/range/type | first_seen_at | 0 | `length(first_seen_at) > 0` |
| 93 | `workflow_architecture_bindings` | length/range/type | home_domain_id | 0 | `length(home_domain_id) BETWEEN 1 AND 256` |
| 94 | `workflow_architecture_bindings` | format/prefix | projection_hash | 0 | `length(projection_hash)=71 AND substr(projection_hash,1,7)='sha256:'` |
| 95 | `workflow_backlog_alignment` | length/range/type | searched | 0 | `length(searched) BETWEEN 2 AND 4096` |
| 96 | `workflow_backlog_alignment` | vocabulary | outcome | 0 | `outcome IN ('related_found','none_found')` |
| 97 | `workflow_backlog_alignment` | cross-column | outcome, related_work_id | 0 | `related_work_id IS NULL OR outcome <> 'none_found'` |
| 98 | `workflow_backlog_alignment` | cross-column | related_work_id, work_id | 0 | `related_work_id IS NULL OR related_work_id <> work_id` |
| 99 | `workflow_checkpoints` | vocabulary | step_kind | 0 | `step_kind IN ('internal_sqlite','cross_authority','external_effect')` |
| 100 | `workflow_checkpoints` | json-shape | result_evidence_refs | 0 | `json_valid(result_evidence_refs) AND json_type(result_evidence_refs)='array'` |
| 101 | `workflow_checkpoints` | length/range/type | checkpoint_id | 0 | `length(checkpoint_id) BETWEEN 2 AND 128` |
| 102 | `workflow_checkpoints` | length/range/type | step_id | 0 | `length(step_id) BETWEEN 2 AND 128` |
| 103 | `workflow_checkpoints` | length/range/type | attempt_epoch | 0 | `attempt_epoch > 0 AND attempt_epoch <= 2147483647` |
| 104 | `workflow_checkpoints` | length/range/type | accepted_inputs_digest | 0 | `length(accepted_inputs_digest) > 0` |
| 105 | `workflow_checkpoints` | json-shape | result_evidence_refs | 0 | `json_array_length(result_evidence_refs) BETWEEN 0 AND 32` |
| 106 | `workflow_checkpoints` | length/range/type | resume_cursor | 0 | `length(resume_cursor) <= 2048` |
| 107 | `workflow_checkpoints` | length/range/type | idempotency_identity | 0 | `length(idempotency_identity) BETWEEN 2 AND 128` |
| 108 | `workflow_checkpoints` | length/range/type | request_id | 0 | `length(request_id) > 0` |
| 109 | `workflow_clarify_records` | length/range/type | work_version | 0 | `work_version > 0` |
| 110 | `workflow_clarify_records` | json-shape | payload | 0 | `json_valid(payload) AND json_type(payload)='object'` |
| 111 | `workflow_contract_affected_domains` | length/range/type | domain_id | 0 | `length(domain_id) BETWEEN 1 AND 256` |
| 112 | `workflow_contract_domain_modifications` | length/range/type | domain_id | 0 | `length(domain_id) BETWEEN 1 AND 256` |
| 113 | `workflow_contract_domain_relation_modifications` | length/range/type | source_domain_id | 0 | `length(source_domain_id) BETWEEN 1 AND 256` |
| 114 | `workflow_contract_domain_relation_modifications` | vocabulary | kind | 0 | `kind IN ('depends_on','shares_contract_with','replaces')` |
| 115 | `workflow_contract_domain_relation_modifications` | length/range/type | target_domain_id | 0 | `length(target_domain_id) BETWEEN 1 AND 256` |
| 116 | `workflow_contract_domain_relation_modifications` | cross-column | source_domain_id, target_domain_id | 0 | `source_domain_id <> target_domain_id` |
| 117 | `workflow_contract_domain_relation_modifications` | cross-column | kind, source_domain_id, target_domain_id | 0 | `kind <> 'shares_contract_with' OR source_domain_id < target_domain_id` |
| 118 | `workflow_contract_law_additions` | length/range/type | law_id | 0 | `length(law_id) BETWEEN 2 AND 256` |
| 119 | `workflow_contract_law_additions` | length/range/type | home_domain_id | 0 | `length(home_domain_id) BETWEEN 1 AND 256` |
| 120 | `workflow_contract_law_additions` | cross-column | reservation_owner_work_id, work_id | 0 | `reservation_owner_work_id = work_id` |
| 121 | `workflow_contract_law_modifications` | length/range/type | law_id | 0 | `length(law_id) BETWEEN 2 AND 256` |
| 122 | `workflow_contract_verification_obligations` | length/range/type | law_id | 0 | `length(law_id) BETWEEN 2 AND 256` |
| 123 | `workflow_contract_verification_obligations` | length/range/type | obligation_id | 0 | `length(obligation_id) BETWEEN 1 AND 256` |
| 124 | `workflow_contracts` | vocabulary | consequence_class | 0 | `consequence_class IN ('internal_sqlite','cross_authority','external_effect')` |
| 125 | `workflow_contracts` | json-shape | spec_mandate | 0 | `json_valid(spec_mandate) AND json_type(spec_mandate)='array'` |
| 126 | `workflow_contracts` | json-shape | law_modifies | 0 | `json_valid(law_modifies) AND json_type(law_modifies)='array' AND json_array_length(law_modifies) BETWEEN 0 AND 32` |
| 127 | `workflow_contracts` | boolean | law_boundary_version | 0 | `law_boundary_version IN (0,1)` |
| 128 | `workflow_contracts` | json-shape | self_repair_json | 0 | `json_valid(self_repair_json) AND json_type(self_repair_json) IN ('null','object')` |
| 129 | `workflow_contracts` | boolean | changes_product_truth | 0 | `changes_product_truth IS NULL OR changes_product_truth IN (0,1)` |
| 130 | `workflow_contracts` | json-shape | legislative_subjects | 0 | `json_valid(legislative_subjects) AND json_type(legislative_subjects)='array'` |
| 131 | `workflow_contracts` | length/range/type | contract_version | 0 | `contract_version > 0 AND contract_version <= 2147483647` |
| 132 | `workflow_contracts` | json-shape | spec_mandate | 0 | `json_array_length(spec_mandate) BETWEEN 0 AND 32` |
| 133 | `workflow_decision_records` | json-shape | options_considered | 0 | `json_valid(options_considered) AND json_type(options_considered)='array'` |
| 134 | `workflow_decision_records` | vocabulary | decision | 0 | `decision IN ('accepted_decision','insufficient_evidence')` |
| 135 | `workflow_decision_records` | json-shape | consequences | 0 | `json_valid(consequences) AND json_type(consequences)='array'` |
| 136 | `workflow_decision_records` | json-shape | inputs | 0 | `json_valid(inputs) AND json_type(inputs)='array'` |
| 137 | `workflow_decision_records` | length/range/type | question | 0 | `length(question) BETWEEN 1 AND 4096` |
| 138 | `workflow_decision_records` | json-shape | options_considered | 0 | `json_array_length(options_considered) BETWEEN 1 AND 16` |
| 139 | `workflow_decision_records` | length/range/type | rationale | 0 | `length(rationale) BETWEEN 1 AND 4096` |
| 140 | `workflow_decision_records` | json-shape | consequences | 0 | `json_array_length(consequences) BETWEEN 1 AND 16` |
| 141 | `workflow_decision_records` | json-shape | inputs | 0 | `json_array_length(inputs) BETWEEN 1 AND 32` |
| 142 | `workflow_decision_records` | length/range/type | poc_findings | 0 | `length(poc_findings) BETWEEN 1 AND 4096` |
| 143 | `workflow_delivery_state` | json-shape | payload | 0 | `json_valid(payload) AND json_type(payload,'$.delivery_artifact')='text' AND length(json_extract(payload,'$.delivery_artifact'))>1 AND json_extract(payload,'$.delivery_state')='asserted'` |
| 144 | `workflow_design_records` | json-shape | decisions | 0 | `json_valid(decisions) AND json_type(decisions)='array'` |
| 145 | `workflow_design_records` | json-shape | touched_refs | 0 | `json_valid(touched_refs) AND json_type(touched_refs)='array'` |
| 146 | `workflow_design_records` | length/range/type | work_version | 0 | `work_version > 0` |
| 147 | `workflow_design_records` | length/range/type | approach | 0 | `length(approach) BETWEEN 2 AND 4096` |
| 148 | `workflow_design_records` | json-shape | decisions | 0 | `json_array_length(decisions) BETWEEN 1 AND 16` |
| 149 | `workflow_design_records` | json-shape | touched_refs | 0 | `json_array_length(touched_refs) BETWEEN 1 AND 64` |
| 150 | `workflow_evidence_bindings` | json-shape | payload | 0 | `json_valid(payload)` |
| 151 | `workflow_impact_edges` | vocabulary | edge_kind | 0 | `edge_kind IN ('modifies','depends_on','forward_link')` |
| 152 | `workflow_impact_edges` | vocabulary | edge_class | 0 | `edge_class IN ('hard','soft','none')` |
| 153 | `workflow_impact_edges` | vocabulary | target_kind | 0 | `target_kind IN ('work_item')` |
| 154 | `workflow_impact_edges` | vocabulary | severity | 0 | `severity IN ('breaking','non-breaking','informational')` |
| 155 | `workflow_impact_edges` | length/range/type | edge_id | 0 | `length(edge_id) BETWEEN 2 AND 128` |
| 156 | `workflow_impact_edges` | cross-column | edge_kind, target_work_id, work_id | 0 | `work_id <> target_work_id OR edge_kind = 'modifies'` |
| 157 | `workflow_instances` | vocabulary | workflow_type | 0 | `workflow_type IS NULL OR workflow_type IN ('change','fix','research','removal')` |
| 158 | `workflow_instances` | json-shape | step_plan | 0 | `step_plan IS NULL OR (json_valid(step_plan) AND json_type(step_plan)='object')` |
| 159 | `workflow_instances` | json-shape | prelude_state | 0 | `prelude_state IS NULL OR (json_valid(prelude_state) AND json_type(prelude_state)='object')` |
| 160 | `workflow_instances` | vocabulary | instance_state | 0 | `instance_state IN ('intake','running','blocked','verifying','completed','cancelled')` |
| 161 | `workflow_instances` | length/range/type | execution_model | 0 | `length(execution_model)<=128` |
| 162 | `workflow_instances` | json-shape | completion_payload | 0 | `completion_payload IS NULL OR json_valid(completion_payload)` |
| 163 | `workflow_law_addition_reservations` | length/range/type | law_id | 0 | `length(law_id) BETWEEN 2 AND 256` |
| 164 | `workflow_law_addition_reservations` | length/range/type | home_domain_id | 0 | `length(home_domain_id) BETWEEN 1 AND 256` |
| 165 | `workflow_native_run_statuses` | boolean | failure | 0 | `failure IN (0,1)` |
| 166 | `workflow_native_runs` | length/range/type | run_id | 0 | `length(run_id) BETWEEN 1 AND 128` |
| 167 | `workflow_native_runs` | vocabulary | phase | 0 | `phase IN ('start','health','rollback','cleanup')` |
| 168 | `workflow_native_runs` | length/range/type | reporting_authority_ref | 0 | `length(reporting_authority_ref) BETWEEN 1 AND 128` |
| 169 | `workflow_native_runs` | length/range/type | actor_ref | 0 | `length(actor_ref) BETWEEN 1 AND 256` |
| 170 | `workflow_native_runs` | length/range/type | native_subject_ref | 0 | `length(native_subject_ref) BETWEEN 1 AND 2048` |
| 171 | `workflow_native_runs` | format/prefix | subject_digest | 0 | `length(subject_digest)=71 AND substr(subject_digest,1,7)='sha256:'` |
| 172 | `workflow_native_runs` | length/range/type | evidence_ref | 0 | `length(evidence_ref) BETWEEN 1 AND 2048` |
| 173 | `workflow_native_runs` | length/range/type | evidence_digest | 0 | `length(evidence_digest) BETWEEN 1 AND 256` |
| 174 | `workflow_native_runs` | fixed-constant | capture_method | 0 | `capture_method='trusted_client_report'` |
| 175 | `workflow_native_runs` | json-shape | observed_universe | 0 | `json_valid(observed_universe) AND json_type(observed_universe)='object'` |
| 176 | `workflow_native_runs` | length/range/type | freshness_policy_ref | 0 | `length(freshness_policy_ref) BETWEEN 1 AND 256` |
| 177 | `workflow_native_runs` | length/range/type | divergence_policy_ref | 0 | `length(divergence_policy_ref) BETWEEN 1 AND 256` |
| 178 | `workflow_native_runs` | vocabulary | verification_state | 0 | `verification_state IN ('unverified','verified','diverged_expected','diverged_unexpected','unverifiable')` |
| 179 | `workflow_overlap_resolutions` | vocabulary | resolution_kind | 0 | `resolution_kind IN ('compatible_with','depends_on','blocks')` |
| 180 | `workflow_overlap_resolutions` | length/range/type | reason | 0 | `length(reason) BETWEEN 1 AND 4096` |
| 181 | `workflow_overlap_resolutions` | cross-column | from_work_id, to_work_id | 0 | `from_work_id<>to_work_id` |
| 182 | `workflow_overlap_resolutions` | cross-column | from_contract_version, to_contract_version | 0 | `from_contract_version>0 AND to_contract_version>0` |
| 183 | `workflow_plan_records` | length/range/type | work_version | 0 | `work_version > 0` |
| 184 | `workflow_plan_records` | json-shape | payload | 0 | `json_valid(payload) AND json_type(payload)='object'` |
| 185 | `workflow_premise_confirmations` | length/range/type | contract_version | 0 | `contract_version > 0` |
| 186 | `workflow_proposal_records` | json-shape | affected | 0 | `json_valid(affected) AND json_type(affected)='array'` |
| 187 | `workflow_proposal_records` | json-shape | user_outcomes | 0 | `json_valid(user_outcomes) AND json_type(user_outcomes)='array'` |
| 188 | `workflow_proposal_records` | json-shape | constraints | 0 | `json_valid(constraints) AND json_type(constraints)='array'` |
| 189 | `workflow_proposal_records` | json-shape | open_questions | 0 | `json_valid(open_questions) AND json_type(open_questions)='array'` |
| 190 | `workflow_proposal_records` | json-shape | out_of_scope | 0 | `json_valid(out_of_scope) AND json_type(out_of_scope)='array'` |
| 191 | `workflow_proposal_records` | length/range/type | work_version | 0 | `work_version > 0` |
| 192 | `workflow_proposal_records` | length/range/type | problem | 0 | `length(problem) BETWEEN 1 AND 4096` |
| 193 | `workflow_proposal_records` | json-shape | affected | 0 | `json_array_length(affected) BETWEEN 1 AND 16` |
| 194 | `workflow_proposal_records` | length/range/type | stakes | 0 | `length(stakes) BETWEEN 1 AND 2048` |
| 195 | `workflow_proposal_records` | json-shape | user_outcomes | 0 | `json_array_length(user_outcomes) BETWEEN 1 AND 16` |
| 196 | `workflow_proposal_records` | json-shape | constraints | 0 | `json_array_length(constraints) BETWEEN 0 AND 16` |
| 197 | `workflow_proposal_records` | json-shape | open_questions | 0 | `json_array_length(open_questions) BETWEEN 0 AND 16` |
| 198 | `workflow_removal_scopes` | length/range/type | work_version | 0 | `work_version > 0` |
| 199 | `workflow_removal_scopes` | json-shape | payload | 0 | `json_valid(payload) AND json_type(payload)='object'` |
| 200 | `workflow_step_epochs` | json-shape | latest_start_payload | 0 | `json_valid(latest_start_payload)` |
| 201 | `write_guard` | fixed-constant | active | 0 | `active = 1` |

## Appendix D — Column inventory (461 columns)

**Reader classes**, in precedence order:
1. **semantic runtime reader**: a runtime production statement reads the column by name.
2. **trigger reader only**: read only by a trigger body (authorizer attribution while preparing writes to every table).
3. **SELECT * only**: reached only through `SELECT *`.
4. **runtime write only**.
5. **dormant-fold SQL only** / **upgrade/migration SQL only** / **fixture SQL only**.
6. **snapshot digest only**.

Every column of every table except `store_revision` is also read by `CurrentStateDigestTx` (`internal/store/internal/sqlitedb/snapshot_state.go:33-61`: `SELECT name FROM main.sqlite_schema WHERE type='table' AND name!='store_revision'`, then `SELECT * FROM main.<table>`). That read only verifies snapshots; it is not a semantic reader and is not counted as one. Retention is cited where law retains a table: `compaction-retention-policy.md` §§1-3; CD-0112 D3 (`.concord/law/decisions/CD-0112-record-delivery-exits-the-step-a-session-executed.md:39`); CD-0224 (`.concord/law/decisions/CD-0224-ten-planning-and-browsing-operations-are-retired-from-the-agent-surface.md:120`). A column without a semantic reader under retention law is classed "retained by law"; the absent reader is not a removal signal.

Counts: semantic runtime reader: 288, runtime write only: 79, dormant-fold SQL only: 34, upgrade/migration SQL only: 11, runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398): 10, snapshot digest only: 9, snapshot digest only — retained by law (compaction-retention-policy.md §2): 8, fixture SQL only — retained by law (compaction-retention-policy.md §§1-3): 7, trigger reader only: 6, runtime write only — retained by law (compaction-retention-policy.md §1 lines 37-38 (reported delivery links stored, no agent read; table mapping inferred from CHECK field delivery_artifact)): 6, snapshot digest only — retained by law (compaction-retention-policy.md §§1-3): 2, dormant-fold SQL only — retained by law (CD-0224 (line 120)): 1.

Trigger readers (observed): `write_guard.active` (140 guard triggers); `work_kinds.kind`/`stored`; `workflow_native_run_statuses.phase`/`status`; `projects.stage_maturity_override`/`stage_audience_commitment_override` (pair rule only; no runtime SQL reads them); `products.display_name` and `projects.display_name`; `archived_work.type`; `archived_work.home_project_id`/`home_locator_id`, `product_knowledge_homes.project_id`/`locator_id` and `project_locators.project_id`/`locator_id` (home-pair triggers); `work_items.kind`; `workflow_native_runs.phase`/`status`.

Coverage: the 461 user columns below. `sqlite_sequence.name` and `sqlite_sequence.seq` (one row: `relations`, 3938) are SQLite-internal AUTOINCREMENT state. They are read and written only by SQLite itself and by migration or retirement SQL (`internal/store/schema.go:6226`, `:9028`; `internal/store/event_log_retirement.go:33`), and are not listed.

Read and write sites are repository paths at the subject commit. The inventory gives an example reader where available; the first runtime writer for each nonzero write count is indexed by table and column immediately after the inventory. Additional sites are not published here.

| Column | Type | NN | Non-null rows | Class | Runtime reads | Runtime writes | Trigger readers | Indexes | FK | CHECK | JSON |
|---|---|---|---:|---|---|---:|---|---:|---|---|---|
| `agent_client_keys.client_ref` | TEXT | Y | 8 | semantic runtime reader | 3 (`internal/store/agent_authority.go:125` …) | 2 | — | 2 | ->agent_clients | — |  |
| `agent_client_keys.key_id` | TEXT | Y | 8 | semantic runtime reader | 1 (`internal/store/agent_authority.go:204`) | 2 | — | 1 | — | length/range/type |  |
| `agent_client_keys.public_key` | BLOB | Y | 8 | semantic runtime reader | 1 (`internal/store/agent_authority.go:204`) | 2 | — | 1 | — | length/range/type |  |
| `agent_client_keys.status` | TEXT | Y | 8 | semantic runtime reader | 3 (`internal/store/agent_authority.go:125` …) | 4 | — | 0 | — | cross-column, vocabulary |  |
| `agent_client_keys.created_at` | TEXT | Y | 8 | runtime write only | 0 | 2 | — | 0 | — | — |  |
| `agent_client_keys.revoked_at` | TEXT |  | 5 | runtime write only | 0 | 2 | — | 0 | — | cross-column |  |
| `agent_clients.client_ref` | TEXT | Y | 3 | semantic runtime reader | 7 (`internal/store/agent_authority.go:119` …) | 1 | — | 1 | — | length/range/type |  |
| `agent_clients.status` | TEXT | Y | 3 | semantic runtime reader | 6 (`internal/store/agent_authority.go:119` …) | 2 | — | 0 | — | cross-column, vocabulary |  |
| `agent_clients.principal_ref` | TEXT | Y | 3 | semantic runtime reader | 2 (`internal/store/agent_authority.go:204` …) | 3 | — | 0 | — | length/range/type |  |
| `agent_clients.capabilities_json` | TEXT | Y | 3 | semantic runtime reader | 2 (`internal/store/agent_authority.go:204` …) | 3 | — | 0 | — | json-shape | Y |
| `agent_clients.product_scope_json` | TEXT | Y | 3 | semantic runtime reader | 2 (`internal/store/agent_authority.go:204` …) | 3 | — | 0 | — | json-shape | Y |
| `agent_clients.project_scope_json` | TEXT | Y | 3 | semantic runtime reader | 2 (`internal/store/agent_authority.go:204` …) | 3 | — | 0 | — | json-shape | Y |
| `agent_clients.created_at` | TEXT | Y | 3 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `agent_clients.rotated_at` | TEXT |  | 1 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `agent_clients.revoked_at` | TEXT |  | 0 | runtime write only | 0 | 1 | — | 0 | — | cross-column |  |
| `agent_clients.agent_scope_json` | TEXT | Y | 3 | semantic runtime reader | 2 (`internal/store/agent_authority.go:204` …) | 3 | — | 0 | — | json-shape | Y |
| `agent_clients.policy_revision` | INTEGER | Y | 3 | semantic runtime reader | 4 (`internal/store/agent_authority.go:204` …) | 2 | — | 0 | — | — |  |
| `agent_installation_keys.key_name` | TEXT | Y | 1 | semantic runtime reader | 2 (`internal/store/internal/sqlitedb/installation_key.go:20` …) | 1 | — | 1 | — | — |  |
| `agent_installation_keys.key_bytes` | BLOB | Y | 1 | semantic runtime reader | 1 (`internal/store/internal/sqlitedb/installation_key.go:82`) | 1 | — | 0 | — | length/range/type |  |
| `agent_installation_keys.created_at` | TEXT | Y | 1 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `agent_nonce_replay.client_ref` | TEXT | Y | 3 | snapshot digest only | 0 | 0 | — | 1 | ->agent_clients | — |  |
| `agent_nonce_replay.nonce` | TEXT | Y | 3 | snapshot digest only | 0 | 0 | — | 1 | — | length/range/type |  |
| `agent_nonce_replay.observed_at` | TEXT | Y | 3 | snapshot digest only | 0 | 0 | — | 0 | — | cross-column |  |
| `agent_nonce_replay.expires_at` | TEXT | Y | 3 | snapshot digest only | 0 | 0 | — | 1 | — | cross-column |  |
| `archived_work.id` | TEXT | Y | 415 | semantic runtime reader | 5 (`internal/store/law_boundary.go:755` …) | 0 | — | 2 | — | — |  |
| `archived_work.type` | TEXT | Y | 415 | semantic runtime reader | 5 (`internal/store/law_boundary.go:755` …) | 0 | archived_work_kind_insert, archived_work_kind_update | 0 | — | — |  |
| `archived_work.title` | TEXT | Y | 415 | fixture SQL only — retained by law (compaction-retention-policy.md §§1-3) | 0 | 0 | — | 0 | — | — |  |
| `archived_work.completed_at` | TEXT | Y | 415 | fixture SQL only — retained by law (compaction-retention-policy.md §§1-3) | 0 | 0 | — | 1 | — | — |  |
| `archived_work.outcome_tag` | TEXT | Y | 415 | semantic runtime reader | 1 (`internal/store/law_boundary.go:755`) | 0 | — | 0 | — | — |  |
| `archived_work.lesson_tags` | TEXT | Y | 415 | fixture SQL only — retained by law (compaction-retention-policy.md §§1-3) | 0 | 0 | — | 0 | — | — | Y |
| `archived_work.terminal_state` | TEXT | Y | 415 | fixture SQL only — retained by law (compaction-retention-policy.md §§1-3) | 0 | 0 | — | 0 | — | vocabulary |  |
| `archived_work.priority` | INTEGER | Y | 415 | fixture SQL only — retained by law (compaction-retention-policy.md §§1-3) | 0 | 0 | — | 0 | — | — |  |
| `archived_work.summary` | TEXT | Y | 415 | fixture SQL only — retained by law (compaction-retention-policy.md §§1-3) | 0 | 0 | — | 0 | — | — |  |
| `archived_work.successor_work_id` | TEXT |  | 0 | fixture SQL only — retained by law (compaction-retention-policy.md §§1-3) | 0 | 0 | — | 0 | — | — |  |
| `archived_work.home_project_id` | TEXT | Y | 415 | semantic runtime reader | 5 (`internal/store/home_pair_binding.go:29` …) | 0 | archived_work_home_pair_bound_insert, archived_work_home_pair_bound_update … | 1 | — | — |  |
| `archived_work.home_locator_id` | TEXT | Y | 415 | semantic runtime reader | 5 (`internal/store/home_pair_binding.go:29` …) | 0 | archived_work_home_pair_bound_insert, archived_work_home_pair_bound_update … | 1 | — | — |  |
| `archived_work.note_path` | TEXT | Y | 415 | semantic runtime reader | 1 (`internal/store/terminal_absence.go:30`) | 0 | — | 0 | — | — |  |
| `archived_work.commit_oid` | TEXT | Y | 415 | semantic runtime reader | 1 (`internal/store/terminal_absence.go:30`) | 0 | — | 0 | — | — |  |
| `archived_work.content_hash` | TEXT | Y | 415 | semantic runtime reader | 1 (`internal/store/terminal_absence.go:30`) | 0 | — | 0 | — | — |  |
| `archived_work.scope_mode` | TEXT | Y | 415 | snapshot digest only — retained by law (compaction-retention-policy.md §§1-3) | 0 | 0 | — | 0 | — | vocabulary |  |
| `archived_work.manifest_schema_version` | TEXT | Y | 415 | snapshot digest only — retained by law (compaction-retention-policy.md §§1-3) | 0 | 0 | — | 0 | — | — |  |
| `archived_work_domains.home_project_id` | TEXT | Y | 27 | snapshot digest only — retained by law (compaction-retention-policy.md §2) | 0 | 0 | — | 1 | ->archived_work | — |  |
| `archived_work_domains.home_locator_id` | TEXT | Y | 27 | snapshot digest only — retained by law (compaction-retention-policy.md §2) | 0 | 0 | — | 1 | ->archived_work | — |  |
| `archived_work_domains.work_id` | TEXT | Y | 27 | snapshot digest only — retained by law (compaction-retention-policy.md §2) | 0 | 0 | — | 2 | ->archived_work | — |  |
| `archived_work_domains.domain_id` | TEXT | Y | 27 | snapshot digest only — retained by law (compaction-retention-policy.md §2) | 0 | 0 | — | 2 | — | length/range/type |  |
| `archived_work_products.home_project_id` | TEXT | Y | 12 | semantic runtime reader | 3 (`internal/store/scope.go:43` …) | 0 | — | 1 | ->archived_work | — |  |
| `archived_work_products.home_locator_id` | TEXT | Y | 12 | semantic runtime reader | 3 (`internal/store/scope.go:43` …) | 0 | — | 1 | ->archived_work | — |  |
| `archived_work_products.work_id` | TEXT | Y | 12 | semantic runtime reader | 3 (`internal/store/scope.go:43` …) | 0 | — | 2 | ->archived_work | — |  |
| `archived_work_products.product_id` | TEXT | Y | 12 | semantic runtime reader | 3 (`internal/store/scope.go:43` …) | 0 | — | 2 | — | — |  |
| `archived_work_projects.home_project_id` | TEXT | Y | 12 | semantic runtime reader | 2 (`internal/store/terminal_absence.go:30` …) | 0 | — | 1 | ->archived_work | — |  |
| `archived_work_projects.home_locator_id` | TEXT | Y | 12 | semantic runtime reader | 2 (`internal/store/terminal_absence.go:30` …) | 0 | — | 1 | ->archived_work | — |  |
| `archived_work_projects.work_id` | TEXT | Y | 12 | semantic runtime reader | 2 (`internal/store/terminal_absence.go:30` …) | 0 | — | 2 | ->archived_work | — |  |
| `archived_work_projects.project_id` | TEXT | Y | 12 | semantic runtime reader | 2 (`internal/store/terminal_absence.go:30` …) | 0 | — | 2 | — | — |  |
| `archived_work_tags.home_project_id` | TEXT | Y | 6 | snapshot digest only — retained by law (compaction-retention-policy.md §2) | 0 | 0 | — | 1 | ->archived_work | — |  |
| `archived_work_tags.home_locator_id` | TEXT | Y | 6 | snapshot digest only — retained by law (compaction-retention-policy.md §2) | 0 | 0 | — | 1 | ->archived_work | — |  |
| `archived_work_tags.work_id` | TEXT | Y | 6 | snapshot digest only — retained by law (compaction-retention-policy.md §2) | 0 | 0 | — | 2 | ->archived_work | — |  |
| `archived_work_tags.tag_id` | TEXT | Y | 6 | snapshot digest only — retained by law (compaction-retention-policy.md §2) | 0 | 0 | — | 2 | — | — |  |
| `domain_observations.observation_id` | TEXT | Y | 316 | semantic runtime reader | 1 (`internal/store/domain_observations.go:195`) | 0 | — | 1 | — | format/prefix |  |
| `domain_observations.product_id` | TEXT | Y | 316 | semantic runtime reader | 1 (`internal/store/domain_observations.go:195`) | 0 | — | 1 | — | — |  |
| `domain_observations.domain_id` | TEXT | Y | 316 | semantic runtime reader | 1 (`internal/store/domain_observations.go:195`) | 0 | — | 1 | — | — |  |
| `domain_observations.statement` | TEXT | Y | 316 | semantic runtime reader | 1 (`internal/store/domain_observations.go:195`) | 0 | — | 0 | — | length/range/type |  |
| `domain_observations.refs` | TEXT | Y | 316 | semantic runtime reader | 1 (`internal/store/domain_observations.go:195`) | 0 | — | 0 | — | json-shape | Y |
| `domain_observations.tags` | TEXT | Y | 316 | semantic runtime reader | 1 (`internal/store/domain_observations.go:195`) | 0 | — | 0 | — | json-shape | Y |
| `domain_observations.state` | TEXT | Y | 316 | semantic runtime reader | 1 (`internal/store/domain_observations.go:195`) | 0 | — | 1 | — | cross-column, vocabulary |  |
| `domain_observations.recorded_at` | TEXT | Y | 316 | semantic runtime reader | 1 (`internal/store/domain_observations.go:195`) | 0 | — | 1 | — | — |  |
| `domain_observations.dismissed_at` | TEXT |  | 13 | dormant-fold SQL only — retained by law (CD-0224 (line 120)) | 0 | 0 | — | 0 | — | cross-column |  |
| `durability_commits.id` | INTEGER | Y | 1 | semantic runtime reader | 1 (`internal/store/internal/sqlitedb/write.go:88`) | 1 | — | 0 | — | fixed-constant |  |
| `durability_commits.bit` | INTEGER | Y | 1 | semantic runtime reader | 1 (`internal/store/internal/sqlitedb/write.go:88`) | 1 | — | 0 | — | boolean |  |
| `durable_operations.op_id` | TEXT | Y | 32,854 | semantic runtime reader | 7 (`internal/store/operation_result.go:40` …) | 1 | — | 1 | — | — |  |
| `durable_operations.attempt_epoch` | INTEGER | Y | 32,854 | semantic runtime reader | 5 (`internal/store/workflow.go:1388` …) | 1 | — | 0 | — | fixed-constant |  |
| `durable_operations.work_id` | TEXT | Y | 32,854 | semantic runtime reader | 4 (`internal/store/operation_result.go:40` …) | 1 | — | 0 | — | — |  |
| `durable_operations.workflow_type_ref` | TEXT | Y | 32,854 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `durable_operations.workflow_type_digest` | TEXT | Y | 32,854 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `durable_operations.step_id` | TEXT | Y | 32,854 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `durable_operations.step_kind` | TEXT | Y | 32,854 | runtime write only | 0 | 1 | — | 0 | — | fixed-constant |  |
| `durable_operations.accepted_inputs_digest` | TEXT | Y | 32,854 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `durable_operations.accepted_scope_snapshot` | TEXT |  | 32,854 | runtime write only | 0 | 1 | — | 0 | — | — | Y |
| `durable_operations.result_kind` | TEXT | Y | 32,854 | semantic runtime reader | 3 (`internal/store/operation_result.go:40` …) | 4 | — | 0 | — | fixed-constant |  |
| `durable_operations.result_payload` | TEXT |  | 32,854 | semantic runtime reader | 1 (`internal/store/operation_result.go:40`) | 2 | — | 0 | — | — | Y |
| `durable_operations.evidence_refs` | TEXT | Y | 32,854 | semantic runtime reader | 2 (`internal/store/operation_result.go:40` …) | 1 | — | 0 | — | — | Y |
| `durable_operations.changed_refs` | TEXT | Y | 32,854 | semantic runtime reader | 1 (`internal/store/operation_result.go:40`) | 2 | — | 0 | — | — | Y |
| `durable_operations.principal_ref` | TEXT | Y | 32,854 | semantic runtime reader | 2 (`internal/store/workflow.go:1388` …) | 1 | — | 0 | — | — |  |
| `durable_operations.request_id` | TEXT | Y | 32,854 | semantic runtime reader | 2 (`internal/store/workflow.go:1388` …) | 1 | — | 0 | — | — |  |
| `durable_operations.observed_at` | TEXT | Y | 32,854 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `durable_operations.completed_at` | TEXT |  | 32,854 | runtime write only | 0 | 2 | — | 0 | — | — |  |
| `durable_operations.contract_digest` | TEXT | Y | 32,854 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `effect_receipts.event_id` | TEXT | Y | 108,765 | semantic runtime reader | 3 (`internal/store/internal/sqlitedb/effect.go:40` …) | 1 | — | 1 | — | — |  |
| `effect_receipts.effect_seq` | INTEGER | Y | 108,765 | semantic runtime reader | 4 (`internal/store/internal/sqlitedb/effect.go:40` …) | 1 | — | 2 | — | — |  |
| `effect_receipts.kind` | TEXT | Y | 108,765 | semantic runtime reader | 2 (`internal/store/internal/sqlitedb/effect.go:40` …) | 1 | — | 0 | — | — |  |
| `effect_receipts.subject_type` | TEXT | Y | 108,765 | semantic runtime reader | 2 (`internal/store/internal/sqlitedb/effect.go:40` …) | 1 | — | 1 | — | — |  |
| `effect_receipts.subject_id` | TEXT | Y | 108,765 | semantic runtime reader | 3 (`internal/store/internal/sqlitedb/effect.go:40` …) | 1 | — | 1 | — | — |  |
| `effect_receipts.actor` | TEXT | Y | 108,765 | semantic runtime reader | 2 (`internal/store/internal/sqlitedb/effect.go:40` …) | 1 | — | 0 | — | — |  |
| `effect_receipts.payload_version` | INTEGER | Y | 108,765 | semantic runtime reader | 1 (`internal/store/internal/sqlitedb/effect.go:40`) | 1 | — | 0 | — | — |  |
| `effect_receipts.payload_digest` | TEXT |  | 108,765 | semantic runtime reader | 1 (`internal/store/internal/sqlitedb/effect.go:40`) | 1 | — | 0 | — | — |  |
| `effect_receipts.relation_seq` | INTEGER |  | 3,718 | semantic runtime reader | 2 (`internal/store/lifecycle.go:745` …) | 1 | — | 1 | — | — |  |
| `effect_receipts.delivery_assertion` | INTEGER | Y | 108,765 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `external_observations.observation_id` | TEXT | Y | 67 | semantic runtime reader | 1 (`internal/store/workflow.go:1439`) | 0 | — | 1 | — | format/prefix |  |
| `external_observations.work_id` | TEXT | Y | 67 | semantic runtime reader | 2 (`internal/store/work_removal.go:440` …) | 0 | — | 1 | ->work_items | — |  |
| `external_observations.subject_kind` | TEXT | Y | 67 | dormant-fold SQL only | 0 | 0 | — | 0 | — | length/range/type |  |
| `external_observations.subject_ref` | TEXT | Y | 67 | dormant-fold SQL only | 0 | 0 | — | 0 | — | length/range/type |  |
| `external_observations.capture_method` | TEXT | Y | 67 | dormant-fold SQL only | 0 | 0 | — | 0 | — | vocabulary |  |
| `external_observations.captured_at` | TEXT | Y | 67 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — |  |
| `external_observations.reporting_authority_ref` | TEXT | Y | 67 | dormant-fold SQL only | 0 | 0 | — | 0 | — | length/range/type |  |
| `external_observations.subject_digest` | TEXT |  | 24 | dormant-fold SQL only | 0 | 0 | — | 0 | — | format/prefix |  |
| `external_observations.observed_universe` | TEXT | Y | 67 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — | Y |
| `external_observations.freshness_policy_ref` | TEXT | Y | 67 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — |  |
| `external_observations.divergence_policy_ref` | TEXT | Y | 67 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — |  |
| `external_observations.verification_state` | TEXT | Y | 67 | semantic runtime reader | 1 (`internal/store/workflow.go:1439`) | 0 | — | 0 | — | vocabulary |  |
| `external_observations.verification_method` | TEXT |  | 60 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — |  |
| `external_observations.verified_at` | TEXT |  | 60 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — |  |
| `external_observations.verifying_authority_ref` | TEXT |  | 60 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — |  |
| `external_observations.verification_result` | TEXT |  | 60 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — |  |
| `external_observations.verification_omissions` | TEXT |  | 60 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — | Y |
| `external_observations.created_event_seq` | INTEGER | Y | 67 | dormant-fold SQL only | 0 | 0 | — | 1 | ->effect_receipts (no child-prefix index) | — |  |
| `external_observations.verified_event_seq` | INTEGER |  | 60 | dormant-fold SQL only | 0 | 0 | — | 0 | ->effect_receipts (no child-prefix index) | — |  |
| `idempotency_records.principal_ref` | TEXT | Y | 47,270 | semantic runtime reader | 3 (`internal/store/agent_mutation_boundary.go:115` …) | 1 | — | 1 | — | — |  |
| `idempotency_records.tool` | TEXT | Y | 47,270 | semantic runtime reader | 3 (`internal/store/agent_mutation_boundary.go:115` …) | 1 | — | 1 | — | — |  |
| `idempotency_records.operation_kind` | TEXT | Y | 47,270 | semantic runtime reader | 3 (`internal/store/agent_mutation_boundary.go:115` …) | 1 | — | 1 | — | — |  |
| `idempotency_records.idempotency_key` | TEXT | Y | 47,270 | semantic runtime reader | 3 (`internal/store/agent_mutation_boundary.go:115` …) | 1 | — | 1 | — | — |  |
| `idempotency_records.canonical_digest` | TEXT | Y | 47,270 | semantic runtime reader | 2 (`internal/store/agent_mutation_boundary.go:115` …) | 1 | — | 0 | — | — |  |
| `idempotency_records.op_id` | TEXT | Y | 47,270 | semantic runtime reader | 2 (`internal/store/agent_mutation_boundary.go:115` …) | 1 | — | 0 | — | — |  |
| `idempotency_records.result_event_ids` | TEXT | Y | 47,270 | semantic runtime reader | 1 (`internal/store/agent_mutation_boundary.go:62`) | 2 | — | 0 | — | — |  |
| `idempotency_records.replayed_count` | INTEGER | Y | 47,270 | semantic runtime reader | 1 (`internal/store/agent_mutation_boundary.go:78`) | 1 | — | 0 | — | — |  |
| `idempotency_records.first_observed_at` | TEXT | Y | 47,270 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `idempotency_records.last_observed_at` | TEXT | Y | 47,270 | runtime write only | 0 | 3 | — | 0 | — | — |  |
| `idempotency_records.result_payload` | TEXT | Y | 47,270 | semantic runtime reader | 2 (`internal/store/agent_mutation_boundary.go:115` …) | 2 | — | 0 | — | — | Y |
| `idempotency_records.changed_refs` | TEXT | Y | 47,270 | semantic runtime reader | 2 (`internal/store/agent_mutation_boundary.go:115` …) | 2 | — | 0 | — | — | Y |
| `idempotency_records.authorized_scope_snapshot` | TEXT | Y | 47,270 | semantic runtime reader | 1 (`internal/store/agent_mutation_boundary.go:62`) | 2 | — | 0 | — | — | Y |
| `linear_issue_links.work_id` | TEXT | Y | 1,849 | semantic runtime reader | 9 (`internal/store/linear_issue_link.go:123` …) | 1 | — | 1 | — | length/range/type |  |
| `linear_issue_links.remote_issue_uuid` | TEXT | Y | 1,849 | semantic runtime reader | 2 (`internal/store/linear_issue_link.go:123` …) | 1 | — | 1 | — | length/range/type |  |
| `linear_issue_links.human_key` | TEXT | Y | 1,849 | semantic runtime reader | 6 (`internal/store/linear_issue_link.go:124` …) | 1 | — | 1 | — | length/range/type |  |
| `linear_issue_links.url` | TEXT | Y | 1,849 | semantic runtime reader | 1 (`internal/store/linear_issue_link.go:30`) | 1 | — | 0 | — | length/range/type |  |
| `linear_issue_links.created_at` | TEXT | Y | 1,849 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `linear_issue_links.updated_at` | TEXT | Y | 1,849 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `product_knowledge_homes.product_id` | TEXT | Y | 12 | semantic runtime reader | 8 (`internal/store/knowledge_home_resolution.go:28` …) | 1 | — | 1 | ->products | — |  |
| `product_knowledge_homes.project_id` | TEXT | Y | 12 | semantic runtime reader | 6 (`internal/store/knowledge_home_resolution.go:28` …) | 1 | product_knowledge_homes_pair_bound_insert, product_knowledge_homes_pair_bound_update | 1 | ->projects | — |  |
| `product_knowledge_homes.locator_id` | TEXT | Y | 12 | semantic runtime reader | 6 (`internal/store/knowledge_home_resolution.go:28` …) | 1 | product_knowledge_homes_pair_bound_insert, product_knowledge_homes_pair_bound_update | 1 | ->project_locators (no child-prefix index) | — |  |
| `product_projects.product_id` | TEXT | Y | 16 | semantic runtime reader | 21 (`internal/store/agent_mutation_boundary.go:153` …) | 1 | — | 3 | ->products | — |  |
| `product_projects.project_id` | TEXT | Y | 16 | semantic runtime reader | 21 (`internal/store/agent_mutation_boundary.go:153` …) | 1 | — | 2 | ->projects | — |  |
| `product_projects.role` | TEXT | Y | 16 | semantic runtime reader | 5 (`internal/store/membership.go:170` …) | 3 | — | 0 | — | vocabulary |  |
| `products.id` | TEXT | Y | 12 | semantic runtime reader | 10 (`internal/store/membership.go:199` …) | 1 | — | 2 | — | — |  |
| `products.display_name` | TEXT | Y | 12 | semantic runtime reader | 2 (`internal/store/query.go:226` …) | 2 | products_display_name_bound_insert, products_display_name_bound_update | 1 | — | — |  |
| `products.stage_maturity` | TEXT | Y | 12 | semantic runtime reader | 2 (`internal/store/query.go:226` …) | 2 | — | 0 | — | vocabulary |  |
| `products.stage_audience_commitment` | TEXT | Y | 12 | semantic runtime reader | 2 (`internal/store/query.go:226` …) | 2 | — | 0 | — | vocabulary |  |
| `products.version` | INTEGER | Y | 12 | semantic runtime reader | 8 (`internal/store/membership.go:199` …) | 3 | — | 0 | — | — |  |
| `products.created_at` | TEXT | Y | 12 | semantic runtime reader | 2 (`internal/store/query.go:226` …) | 1 | — | 0 | — | — |  |
| `products.updated_at` | TEXT | Y | 12 | semantic runtime reader | 2 (`internal/store/query.go:226` …) | 3 | — | 0 | — | — |  |
| `project_handoffs.handoff_id` | TEXT | Y | 24 | semantic runtime reader | 9 (`internal/store/project_handoffs.go:1048` …) | 1 | — | 1 | — | — |  |
| `project_handoffs.work_id` | TEXT | Y | 24 | semantic runtime reader | 13 (`internal/store/project_handoffs.go:1048` …) | 1 | — | 3 | ->work_items | — |  |
| `project_handoffs.contract_version` | INTEGER | Y | 24 | semantic runtime reader | 6 (`internal/store/project_handoffs.go:1071` …) | 1 | — | 0 | — | length/range/type |  |
| `project_handoffs.source_project_id` | TEXT | Y | 24 | semantic runtime reader | 4 (`internal/store/project_handoffs.go:634` …) | 1 | — | 1 | — | cross-column |  |
| `project_handoffs.target_project_id` | TEXT | Y | 24 | semantic runtime reader | 5 (`internal/store/project_handoffs.go:1071` …) | 1 | — | 0 | — | cross-column |  |
| `project_handoffs.source_session_ref` | TEXT | Y | 24 | semantic runtime reader | 5 (`internal/store/project_handoffs.go:634` …) | 1 | — | 1 | — | — |  |
| `project_handoffs.bounded_job` | TEXT | Y | 24 | semantic runtime reader | 1 (`internal/store/project_handoffs.go:634`) | 1 | — | 0 | — | — |  |
| `project_handoffs.changes` | TEXT | Y | 24 | semantic runtime reader | 1 (`internal/store/project_handoffs.go:634`) | 1 | — | 0 | — | json-shape | Y |
| `project_handoffs.verification` | TEXT | Y | 24 | semantic runtime reader | 1 (`internal/store/project_handoffs.go:634`) | 1 | — | 0 | — | json-shape | Y |
| `project_handoffs.artifact_refs` | TEXT | Y | 24 | semantic runtime reader | 2 (`internal/store/project_handoffs.go:634` …) | 1 | — | 0 | — | json-shape | Y |
| `project_handoffs.blockers` | TEXT | Y | 24 | semantic runtime reader | 1 (`internal/store/project_handoffs.go:634`) | 1 | — | 0 | — | json-shape | Y |
| `project_handoffs.next_action` | TEXT | Y | 24 | semantic runtime reader | 1 (`internal/store/project_handoffs.go:634`) | 1 | — | 0 | — | — |  |
| `project_handoffs.state` | TEXT | Y | 24 | semantic runtime reader | 6 (`internal/store/project_handoffs.go:1048` …) | 2 | — | 1 | — | vocabulary |  |
| `project_handoffs.consumed_by_session_ref` | TEXT | Y | 24 | semantic runtime reader | 1 (`internal/store/project_handoffs.go:634`) | 1 | — | 0 | — | — |  |
| `project_handoffs.consumed_at` | TEXT | Y | 24 | semantic runtime reader | 1 (`internal/store/project_handoffs.go:634`) | 1 | — | 0 | — | — |  |
| `project_handoffs.recorded_at` | TEXT | Y | 24 | semantic runtime reader | 1 (`internal/store/project_handoffs.go:634`) | 1 | — | 0 | — | — |  |
| `project_handoffs.recorded_seq` | INTEGER | Y | 24 | semantic runtime reader | 6 (`internal/store/project_handoffs.go:1048` …) | 1 | — | 1 | — | — |  |
| `project_locators.locator_id` | TEXT | Y | 25 | semantic runtime reader | 8 (`internal/store/law_boundary.go:1246` …) | 1 | archived_work_home_pair_bound_insert, archived_work_home_pair_bound_update … | 2 | — | length/range/type |  |
| `project_locators.project_id` | TEXT | Y | 25 | semantic runtime reader | 8 (`internal/store/law_boundary.go:1246` …) | 1 | archived_work_home_pair_bound_insert, archived_work_home_pair_bound_update … | 2 | ->projects | — |  |
| `project_locators.kind` | TEXT | Y | 25 | semantic runtime reader | 6 (`internal/store/law_boundary.go:1246` …) | 2 | — | 2 | — | vocabulary |  |
| `project_locators.locator_value` | TEXT | Y | 25 | semantic runtime reader | 3 (`internal/store/project_locator.go:506` …) | 2 | — | 0 | — | length/range/type |  |
| `project_locators.normalized_value` | TEXT | Y | 25 | semantic runtime reader | 2 (`internal/store/project_locator.go:506` …) | 2 | — | 2 | — | length/range/type |  |
| `project_locators.created_at` | TEXT | Y | 25 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `project_locators.updated_at` | TEXT | Y | 25 | runtime write only | 0 | 2 | — | 0 | — | — |  |
| `project_locators.normalization_version` | INTEGER | Y | 25 | runtime write only | 0 | 2 | — | 0 | — | vocabulary |  |
| `projects.id` | TEXT | Y | 16 | semantic runtime reader | 17 (`internal/store/lifecycle.go:259` …) | 1 | — | 1 | — | — |  |
| `projects.display_name` | TEXT | Y | 16 | semantic runtime reader | 5 (`internal/store/membership.go:330` …) | 1 | projects_display_name_bound_insert, projects_display_name_bound_update | 0 | — | — |  |
| `projects.version` | INTEGER | Y | 16 | semantic runtime reader | 5 (`internal/store/membership.go:199` …) | 3 | — | 0 | — | — |  |
| `projects.created_at` | TEXT | Y | 16 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `projects.updated_at` | TEXT | Y | 16 | runtime write only | 0 | 3 | — | 0 | — | — |  |
| `projects.stage_maturity_override` | TEXT |  | 0 | trigger reader only | 0 | 1 | projects_stage_override_pair_insert, projects_stage_override_pair_update | 0 | — | vocabulary |  |
| `projects.stage_audience_commitment_override` | TEXT |  | 0 | trigger reader only | 0 | 1 | projects_stage_override_pair_insert, projects_stage_override_pair_update | 0 | — | vocabulary |  |
| `projects.repository_mode` | TEXT | Y | 16 | semantic runtime reader | 8 (`internal/store/membership.go:330` …) | 1 | — | 0 | — | vocabulary |  |
| `relations.id` | INTEGER | Y | 2,783 | semantic runtime reader | 3 (`internal/store/q7_current.go:117` …) | 1 | — | 0 | — | — |  |
| `relations.work_id_from` | TEXT | Y | 2,783 | semantic runtime reader | 8 (`internal/store/lifecycle.go:678` …) | 1 | — | 3 | ->work_items | cross-column |  |
| `relations.work_id_to` | TEXT | Y | 2,783 | semantic runtime reader | 8 (`internal/store/lifecycle.go:678` …) | 1 | — | 3 | ->work_items | cross-column |  |
| `relations.kind` | TEXT | Y | 2,783 | semantic runtime reader | 6 (`internal/store/lifecycle.go:678` …) | 1 | — | 3 | — | vocabulary |  |
| `relations.created_at` | TEXT | Y | 2,783 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | — |  |
| `relations.resolution_id` | TEXT |  | 2,357 | semantic runtime reader | 3 (`internal/store/q7_current.go:117` …) | 1 | — | 0 | — | — |  |
| `relations.reason` | TEXT | Y | 2,783 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | — |  |
| `relations.record_version` | INTEGER | Y | 2,783 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | — |  |
| `runtime_state_writers.surface` | TEXT | Y | 5 | upgrade/migration SQL only | 0 | 0 | — | 1 | — | cross-column, vocabulary |  |
| `runtime_state_writers.definition_ref` | TEXT | Y | 5 | snapshot digest only | 0 | 0 | — | 1 | — | — |  |
| `runtime_state_writers.knowledge_projection_version` | INTEGER | Y | 5 | snapshot digest only | 0 | 0 | — | 1 | — | cross-column |  |
| `runtime_state_writers.digest` | TEXT | Y | 5 | snapshot digest only | 0 | 0 | — | 1 | — | — |  |
| `runtime_state_writers.binary_version` | TEXT | Y | 5 | snapshot digest only | 0 | 0 | — | 0 | — | — |  |
| `schema_migrations.version` | INTEGER | Y | 191 | semantic runtime reader | 2 (`internal/store/backup.go:444` …) | 0 | — | 0 | — | — |  |
| `schema_migrations.name` | TEXT | Y | 191 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | — |  |
| `schema_migrations.checksum` | TEXT | Y | 191 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | — |  |
| `schema_migrations.applied_at` | TEXT | Y | 191 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | — |  |
| `schema_migrations.breaking` | INTEGER | Y | 191 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | — |  |
| `store_revision.id` | INTEGER | Y | 1 | semantic runtime reader | 2 (`internal/store/internal/sqlitedb/revision.go:11` …) | 0 | — | 0 | — | fixed-constant |  |
| `store_revision.revision` | INTEGER | Y | 1 | semantic runtime reader | 2 (`internal/store/internal/sqlitedb/revision.go:11` …) | 1 | — | 0 | — | length/range/type |  |
| `store_sequences.name` | TEXT | Y | 2 | semantic runtime reader | 1 (`internal/store/internal/sqlitedb/effect.go:23`) | 0 | — | 1 | — | vocabulary |  |
| `store_sequences.last` | INTEGER | Y | 2 | semantic runtime reader | 1 (`internal/store/internal/sqlitedb/effect.go:23`) | 1 | — | 0 | — | length/range/type |  |
| `work_items.id` | TEXT | Y | 2,378 | semantic runtime reader | 54 (`internal/store/agent_mutation_boundary.go:139` …) | 1 | — | 1 | — | — |  |
| `work_items.kind` | TEXT | Y | 2,378 | semantic runtime reader | 7 (`internal/store/agent_reads.go:31` …) | 2 | work_items_kind_registry_insert, work_items_kind_registry_update | 0 | — | — |  |
| `work_items.title` | TEXT | Y | 2,378 | semantic runtime reader | 4 (`internal/store/agent_reads.go:31` …) | 2 | — | 0 | — | — |  |
| `work_items.lifecycle` | TEXT | Y | 2,378 | semantic runtime reader | 23 (`internal/store/agent_mutation_boundary.go:139` …) | 3 | — | 0 | — | vocabulary |  |
| `work_items.version` | INTEGER | Y | 2,378 | semantic runtime reader | 23 (`internal/store/lifecycle.go:207` …) | 6 | — | 0 | — | — |  |
| `work_items.created_at` | TEXT | Y | 2,378 | semantic runtime reader | 3 (`internal/store/q7_current.go:117` …) | 1 | — | 0 | — | — |  |
| `work_items.updated_at` | TEXT | Y | 2,378 | semantic runtime reader | 2 (`internal/store/query.go:477` …) | 7 | — | 0 | — | — |  |
| `work_items.terminal_time` | TEXT |  | 2,340 | semantic runtime reader | 2 (`internal/store/query.go:477` …) | 2 | — | 0 | — | — |  |
| `work_items.intent_json` | TEXT | Y | 2,378 | semantic runtime reader | 2 (`internal/store/lifecycle.go:525` …) | 2 | — | 0 | — | — | Y |
| `work_items.narrative` | TEXT | Y | 2,378 | semantic runtime reader | 1 (`internal/store/agent_reads.go:31`) | 0 | — | 0 | — | length/range/type |  |
| `work_items.effect_seq` | INTEGER | Y | 2,378 | semantic runtime reader | 3 (`internal/store/q7_current.go:117` …) | 1 | — | 0 | — | length/range/type |  |
| `work_items.last_progress_at` | TEXT | Y | 2,378 | semantic runtime reader | 2 (`internal/store/work_effect_seq.go:31` …) | 1 | — | 0 | — | — |  |
| `work_items.version_actions_floor` | INTEGER | Y | 2,378 | semantic runtime reader | 3 (`internal/store/operation.go:565` …) | 1 | — | 0 | — | — |  |
| `work_items.transition_from` | TEXT |  | 2,362 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 2 | — | 0 | — | — |  |
| `work_items.transition_reason` | TEXT | Y | 2,378 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 2 | — | 0 | — | — |  |
| `work_items.transition_actor` | TEXT | Y | 2,378 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 3 | — | 0 | — | — |  |
| `work_items.transition_evidence_refs` | TEXT | Y | 2,378 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 2 | — | 0 | — | json-shape | Y |
| `work_items.transition_seq` | INTEGER | Y | 2,378 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 3 | — | 0 | — | — |  |
| `work_items.transition_version` | INTEGER | Y | 2,378 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 2 | — | 0 | — | — |  |
| `work_kinds.kind` | TEXT | Y | 7 | trigger reader only | 0 | 0 | work_items_kind_registry_insert, work_items_kind_registry_update | 1 | — | — |  |
| `work_kinds.stored` | INTEGER | Y | 7 | trigger reader only | 0 | 0 | work_items_kind_registry_insert, work_items_kind_registry_update | 0 | — | boolean |  |
| `work_kinds.fold_create` | INTEGER | Y | 7 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | boolean |  |
| `work_kinds.fold_revise` | INTEGER | Y | 7 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | boolean |  |
| `work_kinds.agent_capture` | INTEGER | Y | 7 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | boolean |  |
| `work_projects.work_id` | TEXT | Y | 2,465 | semantic runtime reader | 24 (`internal/store/law_boundary.go:1207` …) | 2 | — | 3 | ->work_items | — |  |
| `work_projects.project_id` | TEXT | Y | 2,465 | semantic runtime reader | 21 (`internal/store/law_boundary.go:1207` …) | 2 | — | 2 | ->projects | — |  |
| `work_projects.role` | TEXT | Y | 2,465 | semantic runtime reader | 10 (`internal/store/law_boundary.go:1207` …) | 4 | — | 0 | — | vocabulary |  |
| `work_removal_operations.operation_id` | TEXT | Y | 3 | semantic runtime reader | 6 (`internal/store/work_removal.go:180` …) | 2 | — | 1 | — | length/range/type |  |
| `work_removal_operations.idempotency_key` | TEXT | Y | 3 | semantic runtime reader | 3 (`internal/store/work_removal.go:188` …) | 2 | — | 1 | — | length/range/type |  |
| `work_removal_operations.work_id` | TEXT | Y | 3 | semantic runtime reader | 6 (`internal/store/work_removal.go:188` …) | 2 | — | 2 | — | length/range/type |  |
| `work_removal_operations.expected_version` | INTEGER | Y | 3 | semantic runtime reader | 2 (`internal/store/work_removal.go:468` …) | 2 | — | 0 | — | length/range/type |  |
| `work_removal_operations.reason` | TEXT | Y | 3 | semantic runtime reader | 1 (`internal/store/work_removal.go:468`) | 2 | — | 0 | — | vocabulary |  |
| `work_removal_operations.actor` | TEXT | Y | 3 | semantic runtime reader | 2 (`internal/store/work_removal.go:351` …) | 2 | — | 0 | — | length/range/type |  |
| `work_removal_operations.product_id` | TEXT | Y | 3 | semantic runtime reader | 1 (`internal/store/work_removal.go:468`) | 2 | — | 0 | — | — |  |
| `work_removal_operations.handoff_json` | TEXT | Y | 3 | semantic runtime reader | 1 (`internal/store/work_removal.go:468`) | 2 | — | 0 | — | json-shape | Y |
| `work_removal_operations.handoff_digest` | TEXT | Y | 3 | semantic runtime reader | 2 (`internal/store/work_removal.go:342` …) | 2 | — | 0 | — | format/prefix |  |
| `work_removal_operations.state` | TEXT | Y | 3 | semantic runtime reader | 5 (`internal/store/work_removal.go:180` …) | 3 | — | 0 | — | vocabulary |  |
| `work_removal_operations.event_id` | TEXT | Y | 3 | semantic runtime reader | 3 (`internal/store/work_removal.go:342` …) | 3 | — | 0 | — | — |  |
| `work_removal_operations.created_at` | TEXT | Y | 3 | runtime write only | 0 | 2 | — | 0 | — | — |  |
| `work_removal_operations.updated_at` | TEXT | Y | 3 | runtime write only | 0 | 3 | — | 0 | — | — |  |
| `work_removal_operations.linear_confirmation_json` | TEXT | Y | 3 | semantic runtime reader | 2 (`internal/store/work_removal.go:342` …) | 2 | — | 0 | — | json-shape | Y |
| `work_version_actions.work_id` | TEXT | Y | 33,494 | semantic runtime reader | 2 (`internal/store/operation.go:571` …) | 1 | — | 1 | ->work_items | — |  |
| `work_version_actions.resulting_version` | INTEGER | Y | 33,494 | semantic runtime reader | 2 (`internal/store/operation.go:571` …) | 1 | — | 1 | — | — |  |
| `work_version_actions.action_id` | TEXT | Y | 33,494 | semantic runtime reader | 1 (`internal/store/operation.go:571`) | 1 | — | 1 | — | — |  |
| `work_version_actions.session_ref` | TEXT | Y | 33,494 | semantic runtime reader | 1 (`internal/store/operation.go:571`) | 1 | — | 0 | — | — |  |
| `work_version_actions.effect_seq` | INTEGER | Y | 33,494 | semantic runtime reader | 1 (`internal/store/operation.go:571`) | 1 | — | 0 | — | — |  |
| `workflow_actors.actor_ref` | TEXT | Y | 12,709 | semantic runtime reader | 9 (`internal/store/project_handoffs.go:986` …) | 1 | — | 1 | — | format/prefix |  |
| `workflow_actors.principal_ref` | TEXT | Y | 12,709 | semantic runtime reader | 2 (`internal/store/workflow.go:776` …) | 1 | — | 1 | — | length/range/type |  |
| `workflow_actors.client_ref` | TEXT | Y | 12,709 | semantic runtime reader | 2 (`internal/store/workflow.go:776` …) | 1 | — | 1 | — | length/range/type |  |
| `workflow_actors.agent_ref` | TEXT | Y | 12,709 | semantic runtime reader | 2 (`internal/store/workflow.go:776` …) | 1 | — | 1 | — | length/range/type |  |
| `workflow_actors.session_ref` | TEXT | Y | 12,709 | semantic runtime reader | 5 (`internal/store/project_handoffs.go:986` …) | 1 | — | 1 | — | length/range/type |  |
| `workflow_actors.actor_class` | TEXT | Y | 12,709 | semantic runtime reader | 5 (`internal/store/workflow.go:1484` …) | 1 | — | 0 | — | vocabulary |  |
| `workflow_actors.first_seen_at` | TEXT | Y | 12,709 | semantic runtime reader | 1 (`internal/store/workpin.go:294`) | 1 | — | 0 | — | length/range/type |  |
| `workflow_architecture_bindings.work_id` | TEXT | Y | 1,942 | semantic runtime reader | 5 (`internal/store/knowledge_authority.go:281` …) | 1 | — | 2 | ->workflow_contracts | — |  |
| `workflow_architecture_bindings.contract_version` | INTEGER | Y | 1,942 | semantic runtime reader | 4 (`internal/store/knowledge_authority.go:281` …) | 1 | — | 2 | ->workflow_contracts | — |  |
| `workflow_architecture_bindings.product_id` | TEXT | Y | 1,942 | semantic runtime reader | 1 (`internal/store/workflow_domain_overlap.go:229`) | 1 | — | 1 | ->products (no child-prefix index) | — |  |
| `workflow_architecture_bindings.home_domain_id` | TEXT | Y | 1,942 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:628`) | 1 | — | 0 | — | length/range/type |  |
| `workflow_architecture_bindings.projection_hash` | TEXT | Y | 1,942 | runtime write only | 0 | 1 | — | 0 | — | format/prefix |  |
| `workflow_backlog_alignment.work_id` | TEXT | Y | 2,050 | semantic runtime reader | 2 (`internal/store/work_removal.go:435` …) | 2 | — | 1 | ->work_items | cross-column |  |
| `workflow_backlog_alignment.related_work_id` | TEXT | Y | 1,255 | semantic runtime reader | 1 (`internal/store/work_removal.go:435`) | 2 | — | 2 | ->work_items | cross-column |  |
| `workflow_backlog_alignment.searched` | TEXT | Y | 2,050 | runtime write only | 0 | 2 | — | 0 | — | length/range/type |  |
| `workflow_backlog_alignment.outcome` | TEXT | Y | 2,050 | runtime write only | 0 | 2 | — | 0 | — | cross-column, vocabulary |  |
| `workflow_backlog_alignment.recorded_at` | TEXT | Y | 2,050 | runtime write only | 0 | 2 | — | 0 | — | — |  |
| `workflow_backlog_alignment.recorded_by` | TEXT | Y | 2,050 | runtime write only | 0 | 2 | — | 0 | ->workflow_actors (no child-prefix index) | — |  |
| `workflow_checkpoints.work_id` | TEXT | Y | 0 | semantic runtime reader | 4 (`internal/store/work_removal.go:440` …) | 1 | — | 3 | ->work_items | — |  |
| `workflow_checkpoints.checkpoint_id` | TEXT | Y | 0 | runtime write only | 0 | 1 | — | 1 | — | length/range/type |  |
| `workflow_checkpoints.step_id` | TEXT | Y | 0 | semantic runtime reader | 2 (`internal/store/workflow.go:972` …) | 1 | — | 1 | — | length/range/type |  |
| `workflow_checkpoints.step_kind` | TEXT | Y | 0 | runtime write only | 0 | 1 | — | 0 | — | vocabulary |  |
| `workflow_checkpoints.attempt_epoch` | INTEGER | Y | 0 | semantic runtime reader | 2 (`internal/store/workflow.go:972` …) | 1 | — | 1 | — | length/range/type |  |
| `workflow_checkpoints.accepted_inputs_digest` | TEXT | Y | 0 | runtime write only | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_checkpoints.result_evidence_refs` | TEXT | Y | 0 | runtime write only | 0 | 1 | — | 0 | — | json-shape | Y |
| `workflow_checkpoints.resume_cursor` | TEXT | Y | 0 | runtime write only | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_checkpoints.idempotency_identity` | TEXT | Y | 0 | runtime write only | 0 | 1 | — | 1 | — | length/range/type |  |
| `workflow_checkpoints.actor_ref` | TEXT | Y | 0 | runtime write only | 0 | 1 | — | 0 | ->workflow_actors (no child-prefix index) | — |  |
| `workflow_checkpoints.request_id` | TEXT | Y | 0 | runtime write only | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_checkpoints.recorded_at` | TEXT | Y | 0 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_clarify_records.work_id` | TEXT | Y | 91 | semantic runtime reader | 4 (`internal/store/work_removal.go:440` …) | 1 | — | 1 | ->work_items | — |  |
| `workflow_clarify_records.work_version` | INTEGER | Y | 91 | runtime write only | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_clarify_records.payload` | TEXT | Y | 91 | semantic runtime reader | 1 (`internal/store/workflow_plan_documents.go:110`) | 1 | — | 0 | — | json-shape | Y |
| `workflow_clarify_records.recorded_at` | TEXT |  | 87 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_clarify_records.actor` | TEXT |  | 87 | runtime write only | 0 | 1 | — | 0 | ->workflow_actors (no child-prefix index) | — |  |
| `workflow_clarify_records.event_ref` | TEXT |  | 87 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_completed_exits.work_id` | TEXT | Y | 16 | semantic runtime reader | 4 (`internal/store/workflow_completed_exits.go:11` …) | 1 | — | 1 | ->work_items | — |  |
| `workflow_completed_exits.definition_ref` | TEXT | Y | 16 | semantic runtime reader | 1 (`internal/store/workflow_completed_exits.go:11`) | 1 | — | 1 | — | — |  |
| `workflow_completed_exits.step_id` | TEXT | Y | 16 | semantic runtime reader | 1 (`internal/store/workflow_completed_exits.go:11`) | 1 | — | 1 | — | — |  |
| `workflow_completed_exits.action_id` | TEXT | Y | 16 | semantic runtime reader | 3 (`internal/store/workflow_completed_exits.go:11` …) | 1 | — | 1 | — | — |  |
| `workflow_completed_exits.exit_seq` | INTEGER | Y | 16 | semantic runtime reader | 1 (`internal/store/workflow_completed_exits.go:11`) | 1 | — | 0 | — | — |  |
| `workflow_contract_affected_domains.work_id` | TEXT | Y | 3,998 | semantic runtime reader | 2 (`internal/store/work_removal.go:440` …) | 1 | — | 2 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_affected_domains.contract_version` | INTEGER | Y | 3,998 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:640`) | 1 | — | 2 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_affected_domains.domain_id` | TEXT | Y | 3,998 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:640`) | 1 | — | 2 | — | length/range/type |  |
| `workflow_contract_domain_modifications.work_id` | TEXT | Y | 764 | semantic runtime reader | 2 (`internal/store/work_removal.go:440` …) | 1 | — | 1 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_domain_modifications.contract_version` | INTEGER | Y | 764 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:643`) | 1 | — | 1 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_domain_modifications.domain_id` | TEXT | Y | 764 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:643`) | 1 | — | 1 | — | length/range/type |  |
| `workflow_contract_domain_relation_modifications.work_id` | TEXT | Y | 18 | semantic runtime reader | 2 (`internal/store/work_removal.go:440` …) | 1 | — | 1 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_domain_relation_modifications.contract_version` | INTEGER | Y | 18 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:646`) | 1 | — | 1 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_domain_relation_modifications.source_domain_id` | TEXT | Y | 18 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:646`) | 1 | — | 1 | — | cross-column, length/range/type |  |
| `workflow_contract_domain_relation_modifications.kind` | TEXT | Y | 18 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:646`) | 1 | — | 1 | — | cross-column, vocabulary |  |
| `workflow_contract_domain_relation_modifications.target_domain_id` | TEXT | Y | 18 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:646`) | 1 | — | 1 | — | cross-column, length/range/type |  |
| `workflow_contract_law_additions.work_id` | TEXT | Y | 116 | semantic runtime reader | 3 (`internal/store/knowledge_authority.go:322` …) | 1 | — | 1 | ->workflow_architecture_bindings (no child-prefix index); ->workflow_architecture_bindings | cross-column |  |
| `workflow_contract_law_additions.contract_version` | INTEGER | Y | 116 | semantic runtime reader | 2 (`internal/store/knowledge_authority.go:322` …) | 1 | — | 1 | ->workflow_architecture_bindings (no child-prefix index); ->workflow_architecture_bindings | — |  |
| `workflow_contract_law_additions.product_id` | TEXT | Y | 116 | runtime write only | 0 | 1 | — | 0 | ->workflow_law_addition_reservations (no child-prefix index); ->workflow_architecture_bindings (no child-prefix index) | — |  |
| `workflow_contract_law_additions.law_id` | TEXT | Y | 116 | semantic runtime reader | 2 (`internal/store/knowledge_authority.go:322` …) | 1 | — | 1 | ->workflow_law_addition_reservations (no child-prefix index) | length/range/type |  |
| `workflow_contract_law_additions.home_domain_id` | TEXT | Y | 116 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:665`) | 1 | — | 0 | ->workflow_law_addition_reservations (no child-prefix index) | length/range/type |  |
| `workflow_contract_law_additions.reservation_owner_work_id` | TEXT | Y | 116 | runtime write only | 0 | 1 | — | 0 | ->workflow_law_addition_reservations (no child-prefix index) | cross-column |  |
| `workflow_contract_law_additions.reservation_owner_contract_version` | INTEGER | Y | 116 | runtime write only | 0 | 1 | — | 0 | ->workflow_law_addition_reservations (no child-prefix index) | — |  |
| `workflow_contract_law_modifications.work_id` | TEXT | Y | 1,287 | semantic runtime reader | 1 (`internal/store/work_removal.go:440`) | 1 | — | 2 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_law_modifications.contract_version` | INTEGER | Y | 1,287 | runtime write only | 0 | 1 | — | 2 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_law_modifications.law_id` | TEXT | Y | 1,287 | runtime write only | 0 | 1 | — | 2 | — | length/range/type |  |
| `workflow_contract_verification_obligations.work_id` | TEXT | Y | 75 | semantic runtime reader | 2 (`internal/store/work_removal.go:440` …) | 1 | — | 1 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_verification_obligations.contract_version` | INTEGER | Y | 75 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:684`) | 1 | — | 1 | ->workflow_architecture_bindings | — |  |
| `workflow_contract_verification_obligations.law_id` | TEXT | Y | 75 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:684`) | 1 | — | 1 | — | length/range/type |  |
| `workflow_contract_verification_obligations.obligation_id` | TEXT | Y | 75 | semantic runtime reader | 1 (`internal/store/workflow_architecture_binding.go:684`) | 1 | — | 1 | — | length/range/type |  |
| `workflow_contracts.work_id` | TEXT | Y | 2,090 | semantic runtime reader | 13 (`internal/store/knowledge_authority.go:242` …) | 1 | — | 1 | ->workflow_contracts (no child-prefix index); ->work_items | — |  |
| `workflow_contracts.contract_version` | INTEGER | Y | 2,090 | semantic runtime reader | 10 (`internal/store/knowledge_authority.go:242` …) | 1 | — | 1 | — | length/range/type |  |
| `workflow_contracts.consequence_class` | TEXT | Y | 2,090 | runtime write only | 0 | 1 | — | 0 | — | vocabulary |  |
| `workflow_contracts.approved_at` | TEXT | Y | 2,090 | semantic runtime reader | 2 (`internal/store/knowledge_authority.go:242` …) | 1 | — | 0 | — | — |  |
| `workflow_contracts.approved_by` | TEXT | Y | 2,090 | semantic runtime reader | 2 (`internal/store/knowledge_authority.go:242` …) | 1 | — | 0 | ->workflow_actors (no child-prefix index) | — |  |
| `workflow_contracts.superseded_by` | INTEGER |  | 536 | semantic runtime reader | 10 (`internal/store/law_boundary.go:291` …) | 1 | — | 0 | ->workflow_contracts (no child-prefix index) | — |  |
| `workflow_contracts.spec_mandate` | TEXT | Y | 2,090 | semantic runtime reader | 3 (`internal/store/law_boundary.go:291` …) | 1 | — | 0 | — | json-shape | Y |
| `workflow_contracts.law_modifies` | TEXT | Y | 2,090 | semantic runtime reader | 4 (`internal/store/knowledge_authority.go:242` …) | 1 | — | 0 | — | json-shape | Y |
| `workflow_contracts.law_boundary_version` | INTEGER | Y | 2,090 | runtime write only | 0 | 1 | — | 0 | — | boolean |  |
| `workflow_contracts.self_repair_json` | TEXT | Y | 2,090 | snapshot digest only | 0 | 0 | — | 0 | — | json-shape | Y |
| `workflow_contracts.supersession_seq` | INTEGER | Y | 2,090 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | — |  |
| `workflow_contracts.last_healthy_seq` | INTEGER | Y | 2,090 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | — |  |
| `workflow_contracts.changes_product_truth` | INTEGER |  | 160 | semantic runtime reader | 2 (`internal/store/knowledge_authority.go:242` …) | 1 | — | 0 | — | boolean |  |
| `workflow_contracts.legislative_subjects` | TEXT | Y | 2,090 | semantic runtime reader | 2 (`internal/store/knowledge_authority.go:290` …) | 1 | — | 0 | — | json-shape | Y |
| `workflow_coordinator_actions.work_id` | TEXT | Y | 5 | semantic runtime reader | 3 (`internal/store/workflow_coordinator_facts.go:22` …) | 1 | — | 1 | ->work_items | — |  |
| `workflow_coordinator_actions.actor_ref` | TEXT | Y | 5 | semantic runtime reader | 2 (`internal/store/workflow_coordinator_facts.go:22` …) | 1 | — | 1 | — | — |  |
| `workflow_coordinator_actions.last_effect_seq` | INTEGER | Y | 5 | semantic runtime reader | 1 (`internal/store/workflow_coordinator_facts.go:22`) | 1 | — | 0 | — | — |  |
| `workflow_coordinator_actions.last_action_id` | TEXT | Y | 5 | semantic runtime reader | 1 (`internal/store/workpin.go:294`) | 1 | — | 0 | — | — |  |
| `workflow_coordinator_actions.last_acted_at` | TEXT | Y | 5 | semantic runtime reader | 2 (`internal/store/workflow_coordinator_facts.go:22` …) | 1 | — | 0 | — | — |  |
| `workflow_decision_records.work_id` | TEXT | Y | 5 | semantic runtime reader | 2 (`internal/store/work_removal.go:440` …) | 1 | — | 1 | ->work_items | — |  |
| `workflow_decision_records.question` | TEXT | Y | 5 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 1 | — | length/range/type |  |
| `workflow_decision_records.options_considered` | TEXT | Y | 5 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 0 | — | json-shape | Y |
| `workflow_decision_records.decision` | TEXT | Y | 5 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 0 | — | vocabulary |  |
| `workflow_decision_records.rationale` | TEXT | Y | 5 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_decision_records.consequences` | TEXT | Y | 5 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 0 | — | json-shape | Y |
| `workflow_decision_records.inputs` | TEXT | Y | 5 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 0 | — | json-shape | Y |
| `workflow_decision_records.poc_findings` | TEXT | Y | 5 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_decision_records.supersedes` | TEXT |  | 0 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 0 | — | — |  |
| `workflow_decision_records.superseded_by` | TEXT |  | 0 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 0 | — | — |  |
| `workflow_decision_records.recorded_at` | TEXT | Y | 5 | runtime write only — retained by law (CD-0112 D3; workflow-engine-contract.md:398) | 0 | 1 | — | 0 | — | — |  |
| `workflow_delivery_state.work_id` | TEXT | Y | 671 | semantic runtime reader | 6 (`internal/store/workflow.go:1164` …) | 1 | — | 1 | ->work_items | — |  |
| `workflow_delivery_state.assertion_seq` | INTEGER | Y | 671 | runtime write only — retained by law (compaction-retention-policy.md §1 lines 37-38 (reported delivery links stored, no agent read; table mapping inferred from CHECK field delivery_artifact)) | 0 | 1 | — | 0 | — | — |  |
| `workflow_delivery_state.assertion_id` | TEXT | Y | 671 | runtime write only — retained by law (compaction-retention-policy.md §1 lines 37-38 (reported delivery links stored, no agent read; table mapping inferred from CHECK field delivery_artifact)) | 0 | 1 | — | 0 | — | — |  |
| `workflow_delivery_state.actor` | TEXT | Y | 671 | runtime write only — retained by law (compaction-retention-policy.md §1 lines 37-38 (reported delivery links stored, no agent read; table mapping inferred from CHECK field delivery_artifact)) | 0 | 1 | — | 0 | — | — |  |
| `workflow_delivery_state.occurred_at` | TEXT | Y | 671 | runtime write only — retained by law (compaction-retention-policy.md §1 lines 37-38 (reported delivery links stored, no agent read; table mapping inferred from CHECK field delivery_artifact)) | 0 | 1 | — | 0 | — | — |  |
| `workflow_delivery_state.payload_version` | INTEGER | Y | 671 | runtime write only — retained by law (compaction-retention-policy.md §1 lines 37-38 (reported delivery links stored, no agent read; table mapping inferred from CHECK field delivery_artifact)) | 0 | 1 | — | 0 | — | — |  |
| `workflow_delivery_state.payload` | TEXT | Y | 671 | runtime write only — retained by law (compaction-retention-policy.md §1 lines 37-38 (reported delivery links stored, no agent read; table mapping inferred from CHECK field delivery_artifact)) | 0 | 1 | — | 0 | — | json-shape | Y |
| `workflow_design_records.work_id` | TEXT | Y | 641 | semantic runtime reader | 1 (`internal/store/work_removal.go:440`) | 0 | — | 2 | ->work_items | — |  |
| `workflow_design_records.work_version` | INTEGER | Y | 641 | dormant-fold SQL only | 0 | 0 | — | 2 | — | length/range/type |  |
| `workflow_design_records.approach` | TEXT | Y | 641 | dormant-fold SQL only | 0 | 0 | — | 0 | — | length/range/type |  |
| `workflow_design_records.decisions` | TEXT | Y | 641 | dormant-fold SQL only | 0 | 0 | — | 0 | — | json-shape | Y |
| `workflow_design_records.touched_refs` | TEXT | Y | 641 | dormant-fold SQL only | 0 | 0 | — | 0 | — | json-shape | Y |
| `workflow_design_records.recorded_at` | TEXT | Y | 641 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — |  |
| `workflow_evidence_bindings.work_id` | TEXT | Y | 10,305 | semantic runtime reader | 2 (`internal/store/q7_current.go:117` …) | 1 | — | 2 | ->work_items | — |  |
| `workflow_evidence_bindings.binding_seq` | INTEGER | Y | 10,305 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 1 | — | — |  |
| `workflow_evidence_bindings.binding_id` | TEXT | Y | 10,305 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 1 | — | — |  |
| `workflow_evidence_bindings.actor` | TEXT | Y | 10,305 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | — |  |
| `workflow_evidence_bindings.payload` | TEXT | Y | 10,305 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | json-shape | Y |
| `workflow_impact_edges.work_id` | TEXT | Y | 26 | semantic runtime reader | 3 (`internal/store/work_removal.go:307` …) | 1 | — | 1 | ->work_items | cross-column |  |
| `workflow_impact_edges.edge_id` | TEXT | Y | 26 | runtime write only | 0 | 1 | — | 1 | — | length/range/type |  |
| `workflow_impact_edges.edge_kind` | TEXT | Y | 26 | semantic runtime reader | 1 (`internal/store/workflow.go:1546`) | 1 | — | 1 | — | cross-column, vocabulary |  |
| `workflow_impact_edges.edge_class` | TEXT | Y | 26 | semantic runtime reader | 1 (`internal/store/workflow.go:1546`) | 1 | — | 1 | — | vocabulary |  |
| `workflow_impact_edges.target_work_id` | TEXT | Y | 26 | semantic runtime reader | 3 (`internal/store/work_removal.go:307` …) | 1 | — | 1 | ->work_items | cross-column |  |
| `workflow_impact_edges.target_kind` | TEXT | Y | 26 | runtime write only | 0 | 1 | — | 0 | — | vocabulary |  |
| `workflow_impact_edges.severity` | TEXT | Y | 26 | runtime write only | 0 | 1 | — | 0 | — | vocabulary |  |
| `workflow_impact_edges.recorded_at` | TEXT | Y | 26 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_instances.work_id` | TEXT | Y | 2,376 | semantic runtime reader | 27 (`internal/store/lifecycle.go:363` …) | 1 | — | 2 | ->work_items | — |  |
| `workflow_instances.workflow_type` | TEXT |  | 148 | semantic runtime reader | 2 (`internal/store/workflow_plan_storage.go:15` …) | 1 | — | 0 | — | vocabulary |  |
| `workflow_instances.step_plan` | TEXT |  | 148 | semantic runtime reader | 3 (`internal/store/lifecycle.go:363` …) | 1 | — | 0 | — | json-shape | Y |
| `workflow_instances.prelude_state` | TEXT |  | 148 | semantic runtime reader | 4 (`internal/store/lifecycle.go:363` …) | 2 | — | 0 | — | json-shape | Y |
| `workflow_instances.instance_state` | TEXT | Y | 2,376 | semantic runtime reader | 13 (`internal/store/lifecycle.go:609` …) | 8 | — | 1 | — | vocabulary |  |
| `workflow_instances.execution_actor_ref` | TEXT |  | 2,113 | semantic runtime reader | 3 (`internal/store/workflow.go:1339` …) | 2 | — | 0 | ->workflow_actors (no child-prefix index) | — |  |
| `workflow_instances.execution_model` | TEXT | Y | 2,376 | runtime write only | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_instances.started_at` | TEXT |  | 1,465 | semantic runtime reader | 1 (`internal/store/workflow_step.go:30`) | 1 | — | 0 | — | — |  |
| `workflow_instances.completed_at` | TEXT |  | 2,329 | runtime write only | 0 | 3 | — | 0 | — | — |  |
| `workflow_instances.last_checkpoint_at` | TEXT |  | 1,498 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_instances.execution_started_at` | TEXT |  | 1,389 | semantic runtime reader | 1 (`internal/store/workflow_step.go:30`) | 1 | — | 0 | — | — |  |
| `workflow_instances.completion_payload` | TEXT |  | 1,104 | runtime write only | 0 | 2 | — | 0 | — | json-shape | Y |
| `workflow_instances.completion_occurred_at` | TEXT |  | 1,104 | runtime write only | 0 | 2 | — | 0 | — | — |  |
| `workflow_law_addition_reservations.product_id` | TEXT | Y | 86 | semantic runtime reader | 3 (`internal/store/cd_reservations.go:24` …) | 1 | — | 3 | ->workflow_architecture_bindings; ->products | — |  |
| `workflow_law_addition_reservations.law_id` | TEXT | Y | 86 | semantic runtime reader | 3 (`internal/store/cd_reservations.go:24` …) | 1 | — | 2 | — | length/range/type |  |
| `workflow_law_addition_reservations.owner_work_id` | TEXT | Y | 86 | semantic runtime reader | 3 (`internal/store/cd_reservations.go:24` …) | 1 | — | 2 | ->workflow_architecture_bindings; ->workflow_contracts (no child-prefix index) | — |  |
| `workflow_law_addition_reservations.owner_contract_version` | INTEGER | Y | 86 | semantic runtime reader | 2 (`internal/store/cd_reservations.go:24` …) | 1 | — | 2 | ->workflow_architecture_bindings; ->workflow_contracts (no child-prefix index) | — |  |
| `workflow_law_addition_reservations.home_domain_id` | TEXT | Y | 86 | semantic runtime reader | 2 (`internal/store/cd_reservations.go:24` …) | 1 | — | 1 | — | length/range/type |  |
| `workflow_native_run_statuses.phase` | TEXT | Y | 10 | trigger reader only | 0 | 0 | workflow_native_runs_status_registry_insert, workflow_native_runs_status_registry_update | 1 | — | — |  |
| `workflow_native_run_statuses.status` | TEXT | Y | 10 | trigger reader only | 0 | 0 | workflow_native_runs_status_registry_insert, workflow_native_runs_status_registry_update | 1 | — | — |  |
| `workflow_native_run_statuses.failure` | INTEGER | Y | 10 | upgrade/migration SQL only | 0 | 0 | — | 0 | — | boolean |  |
| `workflow_native_runs.work_id` | TEXT | Y | 4 | semantic runtime reader | 3 (`internal/store/native_runs.go:200` …) | 0 | — | 2 | ->work_items | — |  |
| `workflow_native_runs.run_id` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 2 | — | length/range/type |  |
| `workflow_native_runs.phase` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | workflow_native_runs_status_registry_insert, workflow_native_runs_status_registry_update | 1 | — | vocabulary |  |
| `workflow_native_runs.status` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | workflow_native_runs_status_registry_insert, workflow_native_runs_status_registry_update | 0 | — | — |  |
| `workflow_native_runs.event_id` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 0 | — | — |  |
| `workflow_native_runs.reporting_authority_ref` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 0 | — | length/range/type |  |
| `workflow_native_runs.actor_ref` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 0 | — | length/range/type |  |
| `workflow_native_runs.native_subject_ref` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 0 | — | length/range/type |  |
| `workflow_native_runs.subject_digest` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 0 | — | format/prefix |  |
| `workflow_native_runs.evidence_ref` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 0 | — | length/range/type |  |
| `workflow_native_runs.evidence_digest` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 0 | — | length/range/type |  |
| `workflow_native_runs.asserted_at` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 0 | — | — |  |
| `workflow_native_runs.recorded_at` | TEXT | Y | 4 | semantic runtime reader | 1 (`internal/store/native_runs.go:200`) | 0 | — | 0 | — | — |  |
| `workflow_native_runs.capture_method` | TEXT | Y | 4 | dormant-fold SQL only | 0 | 0 | — | 0 | — | fixed-constant |  |
| `workflow_native_runs.observed_universe` | TEXT | Y | 4 | dormant-fold SQL only | 0 | 0 | — | 0 | — | json-shape | Y |
| `workflow_native_runs.freshness_policy_ref` | TEXT | Y | 4 | dormant-fold SQL only | 0 | 0 | — | 0 | — | length/range/type |  |
| `workflow_native_runs.divergence_policy_ref` | TEXT | Y | 4 | dormant-fold SQL only | 0 | 0 | — | 0 | — | length/range/type |  |
| `workflow_native_runs.observation_id` | TEXT |  | 4 | semantic runtime reader | 2 (`internal/store/native_runs.go:200` …) | 0 | — | 0 | — | — |  |
| `workflow_native_runs.verification_state` | TEXT | Y | 4 | semantic runtime reader | 2 (`internal/store/native_runs.go:200` …) | 0 | — | 0 | — | vocabulary |  |
| `workflow_overlap_resolutions.resolution_id` | TEXT | Y | 3,127 | semantic runtime reader | 2 (`internal/store/workflow_domain_overlap.go:126` …) | 1 | — | 1 | — | — |  |
| `workflow_overlap_resolutions.event_seq` | INTEGER | Y | 3,127 | runtime write only | 0 | 1 | — | 3 | ->effect_receipts | — |  |
| `workflow_overlap_resolutions.product_id` | TEXT | Y | 3,127 | runtime write only | 0 | 1 | — | 2 | ->products | — |  |
| `workflow_overlap_resolutions.from_work_id` | TEXT | Y | 3,127 | semantic runtime reader | 5 (`internal/store/work_removal.go:435` …) | 1 | — | 2 | ->workflow_contracts (no child-prefix index); ->work_items (no child-prefix index) | cross-column |  |
| `workflow_overlap_resolutions.to_work_id` | TEXT | Y | 3,127 | semantic runtime reader | 5 (`internal/store/work_removal.go:435` …) | 1 | — | 2 | ->workflow_contracts (no child-prefix index); ->work_items (no child-prefix index) | cross-column |  |
| `workflow_overlap_resolutions.from_contract_version` | INTEGER | Y | 3,127 | runtime write only | 0 | 1 | — | 2 | ->workflow_contracts (no child-prefix index) | cross-column |  |
| `workflow_overlap_resolutions.to_contract_version` | INTEGER | Y | 3,127 | runtime write only | 0 | 1 | — | 2 | ->workflow_contracts (no child-prefix index) | cross-column |  |
| `workflow_overlap_resolutions.resolution_kind` | TEXT | Y | 3,127 | runtime write only | 0 | 1 | — | 0 | — | vocabulary |  |
| `workflow_overlap_resolutions.reason` | TEXT | Y | 3,127 | runtime write only | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_overlap_resolutions.created_at` | TEXT | Y | 3,127 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_overlap_resolutions.invalidated_seq` | INTEGER |  | 768 | semantic runtime reader | 4 (`internal/store/workflow_domain_overlap.go:103` …) | 2 | — | 0 | ->effect_receipts (no child-prefix index) | — |  |
| `workflow_phase_exits.work_id` | TEXT | Y | 16 | semantic runtime reader | 3 (`internal/store/q7_current.go:117` …) | 1 | — | 1 | ->work_items | — |  |
| `workflow_phase_exits.action_id` | TEXT | Y | 16 | semantic runtime reader | 2 (`internal/store/q7_current.go:117` …) | 1 | — | 1 | — | — |  |
| `workflow_phase_exits.exit_seq` | INTEGER | Y | 16 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | — |  |
| `workflow_phase_exits.actor` | TEXT | Y | 16 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | — |  |
| `workflow_phase_exits.step_id` | TEXT | Y | 16 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | — |  |
| `workflow_phase_exits.contract_version` | INTEGER | Y | 16 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | — |  |
| `workflow_phase_exits.record_version` | INTEGER | Y | 16 | semantic runtime reader | 1 (`internal/store/q7_current.go:117`) | 1 | — | 0 | — | — |  |
| `workflow_plan_records.work_id` | TEXT | Y | 121 | semantic runtime reader | 5 (`internal/store/work_removal.go:440` …) | 1 | — | 1 | ->work_items | — |  |
| `workflow_plan_records.work_version` | INTEGER | Y | 121 | runtime write only | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_plan_records.payload` | TEXT | Y | 121 | semantic runtime reader | 1 (`internal/store/workflow_plan_documents.go:113`) | 1 | — | 0 | — | json-shape | Y |
| `workflow_plan_records.recorded_at` | TEXT | Y | 121 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_plan_records.actor` | TEXT | Y | 121 | runtime write only | 0 | 1 | — | 0 | ->workflow_actors (no child-prefix index) | — |  |
| `workflow_plan_records.event_ref` | TEXT | Y | 121 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_premise_confirmations.work_id` | TEXT | Y | 1,150 | semantic runtime reader | 5 (`internal/store/work_removal.go:440` …) | 1 | — | 1 | ->workflow_contracts | — |  |
| `workflow_premise_confirmations.contract_version` | INTEGER | Y | 1,150 | semantic runtime reader | 4 (`internal/store/workflow.go:1490` …) | 1 | — | 1 | ->workflow_contracts | length/range/type |  |
| `workflow_premise_confirmations.confirmed_by` | TEXT | Y | 1,150 | runtime write only | 0 | 1 | — | 0 | ->workflow_actors (no child-prefix index) | — |  |
| `workflow_premise_confirmations.confirmed_at` | TEXT | Y | 1,150 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_proposal_records.work_id` | TEXT | Y | 506 | semantic runtime reader | 1 (`internal/store/work_removal.go:440`) | 0 | — | 2 | ->work_items | — |  |
| `workflow_proposal_records.work_version` | INTEGER | Y | 506 | dormant-fold SQL only | 0 | 0 | — | 2 | — | length/range/type |  |
| `workflow_proposal_records.problem` | TEXT | Y | 506 | dormant-fold SQL only | 0 | 0 | — | 0 | — | length/range/type |  |
| `workflow_proposal_records.affected` | TEXT | Y | 506 | dormant-fold SQL only | 0 | 0 | — | 0 | — | json-shape | Y |
| `workflow_proposal_records.stakes` | TEXT | Y | 506 | dormant-fold SQL only | 0 | 0 | — | 0 | — | length/range/type |  |
| `workflow_proposal_records.user_outcomes` | TEXT | Y | 506 | dormant-fold SQL only | 0 | 0 | — | 0 | — | json-shape | Y |
| `workflow_proposal_records.constraints` | TEXT | Y | 506 | dormant-fold SQL only | 0 | 0 | — | 0 | — | json-shape | Y |
| `workflow_proposal_records.open_questions` | TEXT | Y | 506 | dormant-fold SQL only | 0 | 0 | — | 0 | — | json-shape | Y |
| `workflow_proposal_records.recorded_at` | TEXT | Y | 506 | dormant-fold SQL only | 0 | 0 | — | 0 | — | — |  |
| `workflow_proposal_records.out_of_scope` | TEXT | Y | 506 | dormant-fold SQL only | 0 | 0 | — | 0 | — | json-shape | Y |
| `workflow_removal_scopes.work_id` | TEXT | Y | 20 | semantic runtime reader | 3 (`internal/store/work_removal.go:440` …) | 1 | — | 1 | ->work_items | — |  |
| `workflow_removal_scopes.work_version` | INTEGER | Y | 20 | runtime write only | 0 | 1 | — | 0 | — | length/range/type |  |
| `workflow_removal_scopes.payload` | TEXT | Y | 20 | semantic runtime reader | 1 (`internal/store/workflow_plan_documents.go:116`) | 1 | — | 0 | — | json-shape | Y |
| `workflow_removal_scopes.recorded_at` | TEXT | Y | 20 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_removal_scopes.actor` | TEXT | Y | 20 | runtime write only | 0 | 1 | — | 0 | ->workflow_actors (no child-prefix index) | — |  |
| `workflow_removal_scopes.event_ref` | TEXT | Y | 20 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_step_epochs.work_id` | TEXT | Y | 3 | semantic runtime reader | 6 (`internal/store/project_handoffs.go:986` …) | 1 | — | 1 | ->work_items | — |  |
| `workflow_step_epochs.step_id` | TEXT | Y | 3 | semantic runtime reader | 4 (`internal/store/project_handoffs.go:986` …) | 1 | — | 1 | — | — |  |
| `workflow_step_epochs.attempt_epoch` | INTEGER | Y | 3 | runtime write only | 0 | 1 | — | 0 | — | — |  |
| `workflow_step_epochs.latest_start_seq` | INTEGER | Y | 3 | semantic runtime reader | 4 (`internal/store/project_handoffs.go:986` …) | 1 | — | 0 | — | — |  |
| `workflow_step_epochs.latest_start_event_id` | TEXT | Y | 3 | semantic runtime reader | 1 (`internal/store/workflow.go:1305`) | 1 | — | 0 | — | — |  |
| `workflow_step_epochs.latest_start_payload` | TEXT | Y | 3 | semantic runtime reader | 2 (`internal/store/project_handoffs.go:986` …) | 1 | — | 0 | — | json-shape | Y |
| `write_guard.active` | INTEGER | Y | 0 | semantic runtime reader | 1 (`internal/store/internal/sqlitedb/write_guard.go:64`) | 2 | archived_work_domains_guard_delete, archived_work_domains_guard_insert … | 0 | — | fixed-constant |  |

### First runtime writer by column

The following groups list the first extracted runtime writer for every column whose runtime-write count above is nonzero. Columns with a zero count have no extracted runtime writer.

| Table | Columns | First runtime writer |
|---|---|---|
| `agent_client_keys` | `client_ref`, `key_id`, `public_key`, `created_at` | `internal/store/agent_authority.go:128` |
| `agent_client_keys` | `status`, `revoked_at` | `internal/store/agent_authority.go:125` |
| `agent_clients` | `client_ref`, `principal_ref`, `capabilities_json`, `product_scope_json`, `project_scope_json`, `created_at`, `agent_scope_json` | `internal/store/agent_authority.go:42` |
| `agent_clients` | `status`, `revoked_at` | `internal/store/agent_authority.go:148` |
| `agent_clients` | `rotated_at` | `internal/store/agent_authority.go:131` |
| `agent_clients` | `policy_revision` | `internal/store/agent_authority.go:62` |
| `agent_installation_keys` | `key_name`, `key_bytes`, `created_at` | `internal/store/internal/sqlitedb/installation_key.go:49` |
| `durability_commits` | `id`, `bit` | `internal/store/internal/sqlitedb/write.go:88` |
| `durable_operations` | `op_id`, `attempt_epoch`, `work_id`, `workflow_type_ref`, `workflow_type_digest`, `step_id`, `step_kind`, `accepted_inputs_digest`, `accepted_scope_snapshot`, `result_kind`, `principal_ref`, `request_id`, `observed_at`, `contract_digest` | `internal/store/workflow_action_guards.go:109` |
| `durable_operations` | `result_payload`, `changed_refs`, `completed_at` | `internal/store/workflow_action_guards.go:240` |
| `durable_operations` | `evidence_refs` | `internal/store/workflow_action_guards.go:116` |
| `effect_receipts` | `event_id`, `effect_seq`, `kind`, `subject_type`, `subject_id`, `actor`, `payload_version`, `payload_digest`, `relation_seq`, `delivery_assertion` | `internal/store/internal/sqlitedb/effect.go:33` |
| `idempotency_records` | `principal_ref`, `tool`, `operation_kind`, `idempotency_key`, `canonical_digest`, `op_id`, `first_observed_at` | `internal/store/agent_mutation_boundary.go:94` |
| `idempotency_records` | `result_event_ids`, `last_observed_at`, `result_payload`, `changed_refs`, `authorized_scope_snapshot` | `internal/store/agent_mutation_boundary.go:115` |
| `idempotency_records` | `replayed_count` | `internal/store/agent_mutation_boundary.go:78` |
| `linear_issue_links` | `work_id`, `remote_issue_uuid`, `human_key`, `url`, `created_at`, `updated_at` | `internal/store/linear_issue_link.go:106` |
| `product_knowledge_homes` | `product_id`, `project_id`, `locator_id` | `internal/store/product_knowledge_home.go:85` |
| `product_projects` | `product_id`, `project_id`, `role` | `internal/store/membership.go:122` |
| `products` | `id`, `display_name`, `stage_maturity`, `stage_audience_commitment`, `created_at` | `internal/store/operation.go:759` |
| `products` | `version`, `updated_at` | `internal/store/membership.go:216` |
| `project_handoffs` | `handoff_id`, `work_id`, `contract_version`, `source_project_id`, `target_project_id`, `source_session_ref`, `bounded_job`, `changes`, `verification`, `artifact_refs`, `blockers`, `next_action`, `state`, `recorded_at`, `recorded_seq` | `internal/store/project_handoffs.go:174` |
| `project_handoffs` | `consumed_by_session_ref`, `consumed_at` | `internal/store/project_handoffs.go:210` |
| `project_locators` | `locator_id`, `project_id`, `created_at` | `internal/store/project_locator.go:679` |
| `project_locators` | `kind`, `locator_value`, `normalized_value`, `updated_at`, `normalization_version` | `internal/store/project_locator.go:625` |
| `projects` | `id`, `display_name`, `created_at`, `stage_maturity_override`, `stage_audience_commitment_override` | `internal/store/operation.go:841` |
| `projects` | `version`, `updated_at` | `internal/store/membership.go:216` |
| `projects` | `repository_mode` | `internal/store/operation.go:895` |
| `relations` | `id`, `work_id_from`, `work_id_to`, `kind`, `created_at`, `resolution_id`, `reason`, `record_version` | `internal/store/lifecycle.go:725` |
| `store_revision` | `revision` | `internal/store/internal/sqlitedb/revision.go:20` |
| `store_sequences` | `last` | `internal/store/internal/sqlitedb/effect.go:23` |
| `work_items` | `id`, `kind`, `title`, `lifecycle`, `version`, `created_at`, `updated_at`, `terminal_time`, `intent_json`, `transition_actor`, `transition_seq` | `internal/store/lifecycle.go:145` |
| `work_items` | `effect_seq`, `last_progress_at` | `internal/store/work_effect_seq.go:31` |
| `work_items` | `version_actions_floor` | `internal/store/work_version_actions.go:16` |
| `work_items` | `transition_from`, `transition_reason`, `transition_evidence_refs`, `transition_version` | `internal/store/lifecycle.go:577` |
| `work_projects` | `work_id`, `project_id`, `role` | `internal/store/lifecycle.go:280` |
| `work_removal_operations` | `operation_id`, `idempotency_key`, `work_id`, `expected_version`, `reason`, `actor`, `product_id`, `handoff_json`, `handoff_digest`, `state`, `event_id`, `created_at`, `updated_at`, `linear_confirmation_json` | `internal/store/work_removal.go:210` |
| `work_version_actions` | `work_id`, `resulting_version`, `action_id`, `session_ref`, `effect_seq` | `internal/store/work_version_actions.go:10` |
| `workflow_actors` | `actor_ref`, `principal_ref`, `client_ref`, `agent_ref`, `session_ref`, `actor_class`, `first_seen_at` | `internal/store/workflow.go:786` |
| `workflow_architecture_bindings` | `work_id`, `contract_version`, `product_id`, `home_domain_id`, `projection_hash` | `internal/store/workflow_architecture_binding.go:553` |
| `workflow_backlog_alignment` | `work_id`, `related_work_id`, `searched`, `outcome`, `recorded_at`, `recorded_by` | `internal/store/workflow.go:692` |
| `workflow_checkpoints` | `work_id`, `checkpoint_id`, `step_id`, `step_kind`, `attempt_epoch`, `accepted_inputs_digest`, `result_evidence_refs`, `resume_cursor`, `idempotency_identity`, `actor_ref`, `request_id`, `recorded_at` | `internal/store/workflow.go:1008` |
| `workflow_clarify_records` | `work_id`, `work_version`, `payload`, `recorded_at`, `actor`, `event_ref` | `internal/store/workflow_plan_actions.go:130` |
| `workflow_completed_exits` | `work_id`, `definition_ref`, `step_id`, `action_id`, `exit_seq` | `internal/store/workflow_completed_exits.go:11` |
| `workflow_contract_affected_domains` | `work_id`, `contract_version`, `domain_id` | `internal/store/workflow_architecture_binding.go:557` |
| `workflow_contract_domain_modifications` | `work_id`, `contract_version`, `domain_id` | `internal/store/workflow_architecture_binding.go:562` |
| `workflow_contract_domain_relation_modifications` | `work_id`, `contract_version`, `source_domain_id`, `kind`, `target_domain_id` | `internal/store/workflow_architecture_binding.go:567` |
| `workflow_contract_law_additions` | `work_id`, `contract_version`, `product_id`, `law_id`, `home_domain_id`, `reservation_owner_work_id`, `reservation_owner_contract_version` | `internal/store/workflow_architecture_binding.go:581` |
| `workflow_contract_law_modifications` | `work_id`, `contract_version`, `law_id` | `internal/store/workflow.go:631` |
| `workflow_contract_verification_obligations` | `work_id`, `contract_version`, `law_id`, `obligation_id` | `internal/store/workflow_architecture_binding.go:586` |
| `workflow_contracts` | `work_id`, `contract_version`, `consequence_class`, `approved_at`, `approved_by`, `spec_mandate`, `law_modifies`, `law_boundary_version`, `changes_product_truth`, `legislative_subjects` | `internal/store/workflow.go:622` |
| `workflow_contracts` | `superseded_by` | `internal/store/workflow_contract_update.go:90` |
| `workflow_coordinator_actions` | `work_id`, `actor_ref`, `last_effect_seq`, `last_action_id`, `last_acted_at` | `internal/store/workflow_coordinator_facts.go:22` |
| `workflow_decision_records` | `work_id`, `question`, `options_considered`, `decision`, `rationale`, `consequences`, `inputs`, `poc_findings`, `supersedes`, `superseded_by`, `recorded_at` | `internal/store/workflow.go:1075` |
| `workflow_delivery_state` | `work_id`, `assertion_seq`, `assertion_id`, `actor`, `occurred_at`, `payload_version`, `payload` | `internal/store/workflow.go:1223` |
| `workflow_evidence_bindings` | `work_id`, `binding_seq`, `binding_id`, `actor`, `payload` | `internal/store/workflow.go:1450` |
| `workflow_impact_edges` | `work_id`, `edge_id`, `edge_kind`, `edge_class`, `target_work_id`, `target_kind`, `severity`, `recorded_at` | `internal/store/workflow.go:1533` |
| `workflow_instances` | `work_id`, `execution_actor_ref` | `internal/store/workflow_initialization.go:73` |
| `workflow_instances` | `workflow_type`, `step_plan` | `internal/store/workflow_plan_storage.go:62` |
| `workflow_instances` | `prelude_state`, `instance_state`, `completed_at`, `completion_payload`, `completion_occurred_at` | `internal/store/lifecycle.go:363` |
| `workflow_instances` | `execution_model`, `started_at`, `execution_started_at` | `internal/store/workflow_step.go:30` |
| `workflow_instances` | `last_checkpoint_at` | `internal/store/workflow.go:1018` |
| `workflow_law_addition_reservations` | `product_id`, `law_id`, `owner_work_id`, `owner_contract_version`, `home_domain_id` | `internal/store/workflow_architecture_binding.go:573` |
| `workflow_overlap_resolutions` | `resolution_id`, `event_seq`, `product_id`, `from_work_id`, `to_work_id`, `from_contract_version`, `to_contract_version`, `resolution_kind`, `reason`, `created_at` | `internal/store/workflow_domain_overlap.go:250` |
| `workflow_overlap_resolutions` | `invalidated_seq` | `internal/store/workflow_domain_overlap.go:103` |
| `workflow_phase_exits` | `work_id`, `action_id`, `exit_seq`, `actor`, `step_id`, `contract_version`, `record_version` | `internal/store/workflow.go:1231` |
| `workflow_plan_records` | `work_id`, `work_version`, `payload`, `recorded_at`, `actor`, `event_ref` | `internal/store/workflow_plan_documents.go:48` |
| `workflow_premise_confirmations` | `work_id`, `contract_version`, `confirmed_by`, `confirmed_at` | `internal/store/workflow.go:1490` |
| `workflow_removal_scopes` | `work_id`, `work_version`, `payload`, `recorded_at`, `actor`, `event_ref` | `internal/store/workflow_plan_documents.go:66` |
| `workflow_step_epochs` | `work_id`, `step_id`, `attempt_epoch`, `latest_start_seq`, `latest_start_event_id`, `latest_start_payload` | `internal/store/workflow.go:902` |
| `write_guard` | `active` | `internal/store/internal/sqlitedb/write_guard.go:27` |

## Appendix E — Index redundancy and foreign-key coverage (every index is listed in Appendix I)

**Prefix-redundant indexes (observed: 4).** Each is a non-unique, non-partial index whose key columns are a leftmost prefix of another index on the same table:

- `work_removal_operations_work` (work_id) ⊆ `sqlite_autoindex_work_removal_operations_3` (work_id) on `work_removal_operations`
- `workflow_design_records_latest` (work_id, work_version) ⊆ `sqlite_autoindex_workflow_design_records_1` (work_id, work_version) on `workflow_design_records`
- `workflow_native_runs_work` (work_id, run_id) ⊆ `sqlite_autoindex_workflow_native_runs_1` (work_id, run_id, phase) on `workflow_native_runs`
- `workflow_proposal_records_latest` (work_id, work_version) ⊆ `sqlite_autoindex_workflow_proposal_records_1` (work_id, work_version) on `workflow_proposal_records`

**Foreign-key child coverage (observed: 71 foreign keys; 21 without a child index whose leftmost columns are the FK columns).** All 21 are `ON DELETE RESTRICT` (of all 71: 62 RESTRICT, 7 CASCADE, 2 NO ACTION). SQLite probes the child table when a parent row is deleted or its key updated. The plan column below is the EQP of a constructed equivalent child lookup, `SELECT 1 FROM child WHERE <each FK column>=?`, not an EQP of the parent DELETE itself. A probe without an index covering all FK columns may scan or search a shorter leading prefix. Of the nine with runtime parent deletes, seven scan and two search a leading prefix. With `foreign_keys(1)`, modernc's `EXPLAIN QUERY PLAN` of a parent DELETE or INSERT includes these child probes (Appendix H). An index missing from a plan set made without FK enforcement is not evidence that the index is unused.

| Child (columns) → parent | Child rows | Parent runtime delete sites | Equivalent child SELECT plan |
|---|---:|---|---|
| `workflow_overlap_resolutions`(to_work_id, to_contract_version) → `workflow_contracts` | 3,127 | `internal/store/work_removal.go:440` | SCAN workflow_overlap_resolutions USING COVERING INDEX workflow_overlap_resolutions_reverse_pair |
| `workflow_overlap_resolutions`(from_work_id, from_contract_version) → `workflow_contracts` | 3,127 | `internal/store/work_removal.go:440` | SCAN workflow_overlap_resolutions USING COVERING INDEX workflow_overlap_resolutions_reverse_pair |
| `workflow_overlap_resolutions`(to_work_id) → `work_items` | 3,127 | `internal/store/work_removal.go:419` | SCAN workflow_overlap_resolutions USING COVERING INDEX workflow_overlap_resolutions_reverse_pair |
| `workflow_overlap_resolutions`(from_work_id) → `work_items` | 3,127 | `internal/store/work_removal.go:419` | SCAN workflow_overlap_resolutions USING COVERING INDEX workflow_overlap_resolutions_reverse_pair |
| `workflow_contracts`(work_id, superseded_by) → `workflow_contracts` | 2,090 | `internal/store/work_removal.go:440` | SEARCH workflow_contracts USING INDEX sqlite_autoindex_workflow_contracts_1 (work_id=?) |
| `workflow_contract_law_additions`(product_id, law_id, reservation_owner_work_id, reservation_owner_contract_version, home_domain_id) → `workflow_law_addition_reservations` | 116 | `internal/store/work_removal.go:453` | SCAN workflow_contract_law_additions |
| `workflow_contract_law_additions`(work_id, contract_version, product_id) → `workflow_architecture_bindings` | 116 | `internal/store/work_removal.go:440` | SEARCH workflow_contract_law_additions USING INDEX sqlite_autoindex_workflow_contract_law_additions_1 (work_id=? AND contract_version=?) |
| `workflow_law_addition_reservations`(owner_work_id, owner_contract_version) → `workflow_contracts` | 86 | `internal/store/work_removal.go:440` | SCAN workflow_law_addition_reservations USING COVERING INDEX workflow_law_addition_reservations_owner |
| `product_knowledge_homes`(locator_id) → `project_locators` | 12 | `internal/store/project_locator.go:667` | SCAN product_knowledge_homes USING COVERING INDEX sqlite_autoindex_product_knowledge_homes_2 |
| `workflow_overlap_resolutions`(invalidated_seq) → `effect_receipts` | 3,127 | none | SCAN workflow_overlap_resolutions |
| `workflow_instances`(execution_actor_ref) → `workflow_actors` | 2,376 | none | SCAN workflow_instances |
| `workflow_contracts`(approved_by) → `workflow_actors` | 2,090 | none | SCAN workflow_contracts |
| `workflow_backlog_alignment`(recorded_by) → `workflow_actors` | 2,050 | none | SCAN workflow_backlog_alignment |
| `workflow_architecture_bindings`(product_id) → `products` | 1,942 | none | SCAN workflow_architecture_bindings USING COVERING INDEX sqlite_autoindex_workflow_architecture_bindings_2 |
| `workflow_premise_confirmations`(confirmed_by) → `workflow_actors` | 1,150 | none | SCAN workflow_premise_confirmations |
| `workflow_plan_records`(actor) → `workflow_actors` | 121 | none | SCAN workflow_plan_records |
| `workflow_clarify_records`(actor) → `workflow_actors` | 91 | none | SCAN workflow_clarify_records |
| `external_observations`(verified_event_seq) → `effect_receipts` | 67 | none | SCAN external_observations |
| `external_observations`(created_event_seq) → `effect_receipts` | 67 | none | SCAN external_observations USING COVERING INDEX external_observations_work |
| `workflow_removal_scopes`(actor) → `workflow_actors` | 20 | none | SCAN workflow_removal_scopes |
| `workflow_checkpoints`(actor_ref) → `workflow_actors` | 0 | none | SCAN workflow_checkpoints |

## Appendix F — Triggers (static, observed)

169 triggers: 140 write-guard, 12 unconditional abort (immutability or closed registry), 17 conditional abort. Every write-guard check is a 17-opcode probe: `SEARCH write_guard USING INTEGER PRIMARY KEY (rowid=?)`. Conditional triggers are constant-row checks, or a single lookup using a covering index. No timing was measured. The inventories and static probe counts follow; the full trigger bodies remain in the subject schema.

### Write-guard inventory (140)

Every trigger below is in the `write_guard` family and has the same 17-opcode indexed guard probe. They enforce the write seam; no matching Go or CHECK validation of the guard was identified.

| Table | Trigger names |
|---|---|
| `archived_work` | `archived_work_guard_delete`, `archived_work_guard_insert`, `archived_work_guard_update` |
| `archived_work_domains` | `archived_work_domains_guard_delete`, `archived_work_domains_guard_insert`, `archived_work_domains_guard_update` |
| `archived_work_products` | `archived_work_products_guard_delete`, `archived_work_products_guard_insert`, `archived_work_products_guard_update` |
| `archived_work_projects` | `archived_work_projects_guard_delete`, `archived_work_projects_guard_insert`, `archived_work_projects_guard_update` |
| `archived_work_tags` | `archived_work_tags_guard_delete`, `archived_work_tags_guard_insert`, `archived_work_tags_guard_update` |
| `domain_observations` | `domain_observations_guard_delete`, `domain_observations_guard_insert`, `domain_observations_guard_update` |
| `effect_receipts` | `effect_receipts_guard_delete` |
| `external_observations` | `external_observations_guard_delete`, `external_observations_guard_insert`, `external_observations_guard_update` |
| `linear_issue_links` | `linear_issue_links_guard_delete`, `linear_issue_links_guard_insert`, `linear_issue_links_guard_update` |
| `product_knowledge_homes` | `product_knowledge_homes_guard_delete`, `product_knowledge_homes_guard_insert`, `product_knowledge_homes_guard_update` |
| `product_projects` | `product_projects_guard_delete`, `product_projects_guard_insert`, `product_projects_guard_update` |
| `products` | `products_guard_delete`, `products_guard_insert`, `products_guard_update` |
| `project_handoffs` | `project_handoffs_guard_delete`, `project_handoffs_guard_insert`, `project_handoffs_guard_update` |
| `project_locators` | `project_locators_guard_delete`, `project_locators_guard_insert`, `project_locators_guard_update` |
| `projects` | `projects_guard_delete`, `projects_guard_insert`, `projects_guard_update` |
| `relations` | `relations_guard_delete`, `relations_guard_insert`, `relations_guard_update` |
| `work_items` | `work_items_guard_delete`, `work_items_guard_insert`, `work_items_guard_update` |
| `work_projects` | `work_projects_guard_delete`, `work_projects_guard_insert`, `work_projects_guard_update` |
| `work_removal_operations` | `work_removal_operations_guard_delete`, `work_removal_operations_guard_insert`, `work_removal_operations_guard_update` |
| `work_version_actions` | `work_version_actions_guard_delete`, `work_version_actions_guard_insert`, `work_version_actions_guard_update` |
| `workflow_actors` | `workflow_actors_guard_delete`, `workflow_actors_guard_insert` |
| `workflow_architecture_bindings` | `workflow_architecture_bindings_guard_delete`, `workflow_architecture_bindings_guard_insert` |
| `workflow_backlog_alignment` | `workflow_backlog_alignment_guard_delete`, `workflow_backlog_alignment_guard_insert`, `workflow_backlog_alignment_guard_update` |
| `workflow_checkpoints` | `workflow_checkpoints_guard_delete`, `workflow_checkpoints_guard_insert`, `workflow_checkpoints_guard_update` |
| `workflow_clarify_records` | `workflow_clarify_records_guard_delete`, `workflow_clarify_records_guard_insert`, `workflow_clarify_records_guard_update` |
| `workflow_completed_exits` | `workflow_completed_exits_guard_delete`, `workflow_completed_exits_guard_insert`, `workflow_completed_exits_guard_update` |
| `workflow_contract_affected_domains` | `workflow_contract_affected_domains_guard_delete`, `workflow_contract_affected_domains_guard_insert`, `workflow_contract_affected_domains_guard_update` |
| `workflow_contract_domain_modifications` | `workflow_contract_domain_modifications_guard_delete`, `workflow_contract_domain_modifications_guard_insert`, `workflow_contract_domain_modifications_guard_update` |
| `workflow_contract_domain_relation_modifications` | `workflow_contract_domain_relation_modifications_guard_delete`, `workflow_contract_domain_relation_modifications_guard_insert`, `workflow_contract_domain_relation_modifications_guard_update` |
| `workflow_contract_law_additions` | `workflow_contract_law_additions_guard_delete`, `workflow_contract_law_additions_guard_insert`, `workflow_contract_law_additions_guard_update` |
| `workflow_contract_law_modifications` | `workflow_contract_law_modifications_guard_delete`, `workflow_contract_law_modifications_guard_insert`, `workflow_contract_law_modifications_guard_update` |
| `workflow_contract_verification_obligations` | `workflow_contract_verification_obligations_guard_delete`, `workflow_contract_verification_obligations_guard_insert`, `workflow_contract_verification_obligations_guard_update` |
| `workflow_contracts` | `workflow_contracts_guard_delete`, `workflow_contracts_guard_insert`, `workflow_contracts_guard_update` |
| `workflow_coordinator_actions` | `workflow_coordinator_actions_guard_delete`, `workflow_coordinator_actions_guard_insert`, `workflow_coordinator_actions_guard_update` |
| `workflow_decision_records` | `workflow_decision_records_guard_delete`, `workflow_decision_records_guard_insert`, `workflow_decision_records_guard_update` |
| `workflow_delivery_state` | `workflow_delivery_state_guard_delete`, `workflow_delivery_state_guard_insert`, `workflow_delivery_state_guard_update` |
| `workflow_design_records` | `workflow_design_records_guard_delete`, `workflow_design_records_guard_insert` |
| `workflow_evidence_bindings` | `workflow_evidence_bindings_guard_delete`, `workflow_evidence_bindings_guard_insert`, `workflow_evidence_bindings_guard_update` |
| `workflow_impact_edges` | `workflow_impact_edges_guard_delete`, `workflow_impact_edges_guard_insert`, `workflow_impact_edges_guard_update` |
| `workflow_instances` | `workflow_instances_guard_delete`, `workflow_instances_guard_insert`, `workflow_instances_guard_update` |
| `workflow_law_addition_reservations` | `workflow_law_addition_reservations_guard_delete`, `workflow_law_addition_reservations_guard_insert` |
| `workflow_native_runs` | `workflow_native_runs_guard_delete`, `workflow_native_runs_guard_insert`, `workflow_native_runs_guard_update` |
| `workflow_overlap_resolutions` | `workflow_overlap_resolutions_guard_delete`, `workflow_overlap_resolutions_guard_insert`, `workflow_overlap_resolutions_guard_update` |
| `workflow_phase_exits` | `workflow_phase_exits_guard_delete`, `workflow_phase_exits_guard_insert`, `workflow_phase_exits_guard_update` |
| `workflow_plan_records` | `workflow_plan_records_guard_delete`, `workflow_plan_records_guard_insert`, `workflow_plan_records_guard_update` |
| `workflow_premise_confirmations` | `workflow_premise_confirmations_guard_delete`, `workflow_premise_confirmations_guard_insert`, `workflow_premise_confirmations_guard_update` |
| `workflow_proposal_records` | `workflow_proposal_records_guard_delete`, `workflow_proposal_records_guard_insert` |
| `workflow_removal_scopes` | `workflow_removal_scopes_guard_delete`, `workflow_removal_scopes_guard_insert`, `workflow_removal_scopes_guard_update` |
| `workflow_step_epochs` | `workflow_step_epochs_guard_delete`, `workflow_step_epochs_guard_insert`, `workflow_step_epochs_guard_update` |

### Unconditional abort inventory (12)

Each trigger below aborts unconditionally and has a five-opcode static probe. No matching Go or CHECK validation was identified for these unconditional database prohibitions.

| Trigger | Table | Event |
|---|---|---|
| `effect_receipts_no_update` | `effect_receipts` | UPDATE |
| `work_kinds_registry_no_delete` | `work_kinds` | DELETE |
| `work_kinds_registry_no_insert` | `work_kinds` | INSERT |
| `work_kinds_registry_no_update` | `work_kinds` | UPDATE |
| `workflow_actors_guard_update` | `workflow_actors` | UPDATE |
| `workflow_architecture_bindings_guard_update` | `workflow_architecture_bindings` | UPDATE |
| `workflow_design_records_guard_update` | `workflow_design_records` | UPDATE |
| `workflow_law_addition_reservations_guard_update` | `workflow_law_addition_reservations` | UPDATE |
| `workflow_native_run_statuses_registry_no_delete` | `workflow_native_run_statuses` | DELETE |
| `workflow_native_run_statuses_registry_no_insert` | `workflow_native_run_statuses` | INSERT |
| `workflow_native_run_statuses_registry_no_update` | `workflow_native_run_statuses` | UPDATE |
| `workflow_proposal_records_guard_update` | `workflow_proposal_records` | UPDATE |

### Conditional abort inventory (17)

Each row identifies an individual `conditional_abort` trigger and its static probe cost. Matching Go checks are described by rule below; "none found" is limited to the examined Go and CHECK evidence.

| Trigger | Table | Event | Probe opcodes | Matching Go or CHECK |
|---|---|---|---:|---|
| `archived_work_home_pair_bound_insert` | `archived_work` | INSERT | 22 | none found |
| `archived_work_home_pair_bound_update` | `archived_work` | UPDATE | 22 | none found |
| `archived_work_kind_insert` | `archived_work` | INSERT | 43 | none found |
| `archived_work_kind_update` | `archived_work` | UPDATE | 43 | none found |
| `product_knowledge_homes_pair_bound_insert` | `product_knowledge_homes` | INSERT | 22 | none found |
| `product_knowledge_homes_pair_bound_update` | `product_knowledge_homes` | UPDATE | 22 | none found |
| `products_display_name_bound_insert` | `products` | INSERT | 12 | `validDisplayName` |
| `products_display_name_bound_update` | `products` | UPDATE | 12 | `validDisplayName` |
| `project_locators_referenced_by_knowledge_no_delete` | `project_locators` | DELETE | 18 | `locatorReferencedByKnowledge` |
| `projects_display_name_bound_insert` | `projects` | INSERT | 12 | `validDisplayName` |
| `projects_display_name_bound_update` | `projects` | UPDATE | 12 | `validDisplayName` |
| `projects_stage_override_pair_insert` | `projects` | INSERT | 14 | `decodeProjectStagePair` |
| `projects_stage_override_pair_update` | `projects` | UPDATE | 14 | `decodeProjectStagePair` |
| `work_items_kind_registry_insert` | `work_items` | INSERT | 25 | `WorkKindStored` / `WorkKindFoldCreateAllowed` |
| `work_items_kind_registry_update` | `work_items` | UPDATE | 25 | `WorkKindStored` / `WorkKindFoldCreateAllowed` |
| `workflow_native_runs_status_registry_insert` | `workflow_native_runs` | INSERT | 22 | `NativeRunStatusAllowed` |
| `workflow_native_runs_status_registry_update` | `workflow_native_runs` | UPDATE | 22 | `NativeRunStatusAllowed` |

| Conditional rule (triggers) | Matching Go check (observed) |
|---|---|
| display-name length 1–256 (4) | `validDisplayName`, `internal/store/operation.go:735-744`. The comment states it agrees with the migration 50 trigger |
| project stage-override pair (2) | `decodeProjectStagePair`, `internal/store/operation.go:671-677` |
| work kind registry `stored=1` (2) | `WorkKindStored` / `WorkKindFoldCreateAllowed`, `internal/store/generated_work_kinds.go:54-72`; used at `internal/store/lifecycle.go:122` |
| native-run status registry (2) | `NativeRunStatusAllowed`, `internal/store/native_runs.go:66` |
| locator referenced by knowledge, no delete (1) | `locatorReferencedByKnowledge`, `internal/store/home_pair_binding.go:27-40`; used at `internal/store/project_locator.go:659` |
| archived_work home pair bound (2), knowledge-home pair bound (2), archived_work type vocabulary (2) | none found |

Whether a pair of matching checks is deliberate defence in depth or a redundancy is not settled by this evidence.

## Appendix G — Idempotency records (observed)

- 46 stored tool/operation pairs. 10 are current mutation pairs in `contracts/agent-tool-surface.v1.json` (39,853 rows). 36 are absent from the manifest: 7,417 rows, 17,492,638 B (16.68 MiB) of values. Absence from the manifest does not by itself end replay authority. Law: `.concord/law/specs/agent-call-context-contract.md:294-297` (no calendar TTL).
- 41,096 rows lack `authorized_scope_snapshot.mutation_response`; their `result_payload` totals 164,520,297 B (156.90 MiB). 34,278 of these rows belong to current pairs (153,318,862 B (146.22 MiB)). `retainedMutationResponse` (`internal/agent/idempotent_response.go`) refuses a snapshot without it. The first row carrying the field is at `2026-10-26T20:32:06.198545077Z`; no row after that lacks it.
- 1,295 rows have `result_payload='{}'`, first `2026-10-13T17:10:12.091106773Z`, last `2026-10-25T17:36:21.705182441Z`. 1,292 link to a durable operation; all of those have `result_kind='completed'`. *Inference*: placeholders written by an earlier version; corruption is not established.
- 7 `workflow_action` rows have no durable-operation link. Every one names removed work with a committed `work_removal_operations` row. `RequireActionableWork` runs before the idempotency lookup (`internal/agent/mutations.go`; `internal/store/planning_retirement.go`), so a retry for removed work never reaches the lookup. That does not end the record's authority.
- Overlap invalidation runs on resolution and on reopen, not only on cleanup: `internal/store/workflow_domain_overlap.go:91-132`, `internal/store/lifecycle.go:327-358`. `resolve_overlap` holds 3,133 idempotency rows.

The figures above give the aggregates used by the findings; the full per-pair breakdown is not published here.

## Appendix H — Query-plan coverage (observed)

360 runtime production statements prepare successfully. The 49 statements with no plan in the earlier plan set are all `INSERT … VALUES`, which have no read plan, so they are not coverage gaps. Re-planned with modernc 3.54.1 and `foreign_keys(1)`, 8 of the 49 show FK-enforcement child probes, each guarded by `FkIfZero` (for example, an insert into `work_items` compiles 28 `FkIfZero`):

- `internal/store/lifecycle.go:145` (foldWorkCreated): SEARCH workflow_contracts USING COVERING INDEX sqlite_autoindex_workflow_contracts_1 (work_id=?); SEARCH workflow_clarify_records USING COVERING INDEX sqlite_autoindex_workflow_clarify_records_1 (work_id=?); SCAN workflow_overlap_resolutions USING COVERING INDEX workflow_overlap_resolutions_reverse_pair …
- `internal/store/operation.go:759` (foldProductCreated): SEARCH workflow_overlap_resolutions USING COVERING INDEX workflow_overlap_resolutions_reverse_pair (product_id=?); SEARCH workflow_law_addition_reservations USING COVERING INDEX workflow_law_addition_reservations_owner (product_id=?); SCAN workflow_architecture_bindings USING COVERING INDEX sqlite_autoindex_workflow_architecture_bindings_2 …
- `internal/store/operation.go:841` (foldProjectCreated): SEARCH product_knowledge_homes USING COVERING INDEX sqlite_autoindex_product_knowledge_homes_2 (project_id=?); SEARCH project_locators USING COVERING INDEX project_locators_project_locator (project_id=?); SEARCH work_projects USING COVERING INDEX work_projects_by_project (project_id=?) …
- `internal/store/project_locator.go:679` (insertProjectLocator): SCAN product_knowledge_homes USING COVERING INDEX sqlite_autoindex_product_knowledge_homes_2
- `internal/store/workflow.go:622` (persistWorkflowContractApprovalTx): SEARCH workflow_contracts USING INDEX sqlite_autoindex_workflow_contracts_1 (work_id=?); SCAN workflow_overlap_resolutions USING COVERING INDEX workflow_overlap_resolutions_reverse_pair; SCAN workflow_overlap_resolutions USING COVERING INDEX workflow_overlap_resolutions_reverse_pair …
- `internal/store/workflow.go:786` (persistWorkflowActorTuple): SCAN workflow_contracts; SCAN workflow_clarify_records; SCAN workflow_instances …
- `internal/store/workflow_architecture_binding.go:553` (persistWorkflowArchitectureBindingTx): SEARCH workflow_contract_law_modifications USING COVERING INDEX sqlite_autoindex_workflow_contract_law_modifications_1 (work_id=? AND contract_version=?); SEARCH workflow_contract_verification_obligations USING COVERING INDEX sqlite_autoindex_workflow_contract_verification_obligations_1 (work_id=? AND contract_version=?); SEARCH workflow_contract_law_additions USING INDEX sqlite_autoindex_workflow_contract_law_additions_1 (work_id=? AND contract_version=?) …
- `internal/store/workflow_architecture_binding.go:573` (persistWorkflowArchitectureBindingTx): SCAN workflow_contract_law_additions

*Inference, not verified by stepping*: SQLite skips a parent-side probe guarded by `FkIfZero` when no FK violation is outstanding. 35 statements plan differently from the earlier plan set (16 of them had an empty plan there). The earlier harness opened Python SQLite 3.46.1 without enabling `foreign_keys`, so its connection used the default off setting. The replan used modernc SQLite 3.54.1 with `foreign_keys` on; both engine version and enforcement setting differ.

## Appendix I — All 110 indexes

**Columns.** Bytes are `dbstat` page bytes on the backup. Role: PK = automatic primary-key index; UNIQUE = automatic UNIQUE-constraint index; explicit = `CREATE INDEX` (partial where marked); FK = the index's leftmost columns cover a foreign key's child columns. "Plan hits" counts the distinct runtime production statement sites whose extracted plan selects the index: once in the earlier plan set, and once in the production-driver set with `foreign_keys(1)`, which also shows FK-enforcement probes.

**Reading the hit counts.** A hit count shows only that a plan selected the index. Zero does not prove an index unused: the statement set is static, data-dependent statement shapes may be missing, and an index can also enforce a constraint. Automatic indexes remain constraints whatever their hit count. Apart from the 4 prefix-redundant indexes (R13), no zero-hit index is recommended for deletion.

| Index | Table | Key columns | Bytes | Role | FK | Plan hits (earlier set) | Plan hits (FK-on set) | Note |
|---|---|---|---:|---|---|---:|---:|---|
| `agent_client_one_active_key` | `agent_client_keys` | client_ref | 4,096 | explicit, partial | yes | 3 | 3 |  |
| `sqlite_autoindex_agent_client_keys_1` | `agent_client_keys` | client_ref,key_id | 4,096 | PK | yes | 0 | 0 |  |
| `sqlite_autoindex_agent_client_keys_2` | `agent_client_keys` | public_key | 4,096 | UNIQUE |  | 0 | 0 |  |
| `sqlite_autoindex_agent_clients_1` | `agent_clients` | client_ref | 4,096 | PK |  | 7 | 7 |  |
| `sqlite_autoindex_agent_installation_keys_1` | `agent_installation_keys` | key_name | 4,096 | PK |  | 2 | 2 |  |
| `agent_nonce_replay_expiry` | `agent_nonce_replay` | expires_at | 4,096 | explicit |  | 0 | 0 |  |
| `sqlite_autoindex_agent_nonce_replay_1` | `agent_nonce_replay` | client_ref,nonce | 4,096 | PK | yes | 0 | 0 |  |
| `archived_work_completed_order` | `archived_work` | completed_at,id | 24,576 | explicit |  | 0 | 0 |  |
| `sqlite_autoindex_archived_work_1` | `archived_work` | home_project_id,home_locator_id,id | 32,768 | PK |  | 4 | 4 |  |
| `archived_work_domains_lookup` | `archived_work_domains` | domain_id,work_id | 4,096 | explicit |  | 0 | 0 |  |
| `sqlite_autoindex_archived_work_domains_1` | `archived_work_domains` | home_project_id,home_locator_id,work_id,domain_id | 4,096 | PK | yes | 0 | 0 |  |
| `archived_work_products_lookup` | `archived_work_products` | product_id,work_id | 4,096 | explicit |  | 1 | 1 |  |
| `sqlite_autoindex_archived_work_products_1` | `archived_work_products` | home_project_id,home_locator_id,work_id,product_id | 4,096 | PK | yes | 2 | 2 |  |
| `archived_work_projects_lookup` | `archived_work_projects` | project_id,work_id | 4,096 | explicit |  | 0 | 0 |  |
| `sqlite_autoindex_archived_work_projects_1` | `archived_work_projects` | home_project_id,home_locator_id,work_id,project_id | 4,096 | PK | yes | 2 | 2 |  |
| `archived_work_tags_lookup` | `archived_work_tags` | tag_id,work_id | 4,096 | explicit |  | 0 | 0 |  |
| `sqlite_autoindex_archived_work_tags_1` | `archived_work_tags` | home_project_id,home_locator_id,work_id,tag_id | 4,096 | PK | yes | 0 | 0 |  |
| `domain_observations_open` | `domain_observations` | product_id,domain_id,state,recorded_at | 28,672 | explicit |  | 1 | 1 |  |
| `sqlite_autoindex_domain_observations_1` | `domain_observations` | observation_id | 16,384 | PK |  | 0 | 0 |  |
| `sqlite_autoindex_durable_operations_1` | `durable_operations` | op_id | 1,519,616 | PK |  | 7 | 7 |  |
| `effect_receipts_relation_seq` | `effect_receipts` | relation_seq | 45,056 | explicit, partial |  | 1 | 1 |  |
| `effect_receipts_subject` | `effect_receipts` | subject_type,subject_id,effect_seq | 6,623,232 | explicit |  | 1 | 1 |  |
| `sqlite_autoindex_effect_receipts_1` | `effect_receipts` | event_id | 8,024,064 | PK |  | 3 | 3 |  |
| `sqlite_autoindex_effect_receipts_2` | `effect_receipts` | effect_seq | 1,429,504 | UNIQUE |  | 0 | 0 |  |
| `external_observations_work` | `external_observations` | work_id,created_event_seq | 4,096 | explicit | yes | 1 | 3 |  |
| `sqlite_autoindex_external_observations_1` | `external_observations` | observation_id | 4,096 | PK |  | 1 | 1 |  |
| `sqlite_autoindex_idempotency_records_1` | `idempotency_records` | principal_ref,tool,operation_kind,idempotency_key | 5,423,104 | PK |  | 3 | 3 |  |
| `linear_issue_links_human_key` | `linear_issue_links` | human_key | 36,864 | explicit |  | 1 | 1 |  |
| `linear_issue_links_remote_uuid` | `linear_issue_links` | remote_issue_uuid | 98,304 | explicit |  | 1 | 1 |  |
| `sqlite_autoindex_linear_issue_links_1` | `linear_issue_links` | work_id | 81,920 | PK |  | 6 | 6 |  |
| `sqlite_autoindex_product_knowledge_homes_1` | `product_knowledge_homes` | product_id | 4,096 | PK | yes | 5 | 6 |  |
| `sqlite_autoindex_product_knowledge_homes_2` | `product_knowledge_homes` | project_id,locator_id | 4,096 | UNIQUE | yes | 3 | 6 |  |
| `product_projects_by_project` | `product_projects` | project_id,product_id | 4,096 | explicit | yes | 11 | 12 |  |
| `product_projects_one_primary` | `product_projects` | product_id | 4,096 | explicit, partial | yes | 1 | 1 |  |
| `sqlite_autoindex_product_projects_1` | `product_projects` | product_id,project_id | 4,096 | PK | yes | 11 | 12 |  |
| `products_display_name_order` | `products` | display_name,id | 4,096 | explicit |  | 1 | 1 |  |
| `sqlite_autoindex_products_1` | `products` | id | 4,096 | PK |  | 9 | 9 |  |
| `project_handoffs_recorded_seq` | `project_handoffs` | work_id,recorded_seq | 4,096 | explicit | yes | 8 | 10 |  |
| `project_handoffs_source` | `project_handoffs` | work_id,source_project_id,source_session_ref | 4,096 | explicit | yes | 1 | 1 |  |
| `project_handoffs_work_state` | `project_handoffs` | work_id,state | 4,096 | explicit | yes | 0 | 0 |  |
| `sqlite_autoindex_project_handoffs_1` | `project_handoffs` | handoff_id | 4,096 | PK |  | 4 | 4 |  |
| `project_locators_by_project` | `project_locators` | project_id,kind,normalized_value | 4,096 | explicit | yes | 1 | 1 |  |
| `project_locators_project_locator` | `project_locators` | project_id,locator_id | 4,096 | explicit | yes | 5 | 6 |  |
| `sqlite_autoindex_project_locators_1` | `project_locators` | locator_id | 4,096 | PK |  | 1 | 1 |  |
| `sqlite_autoindex_project_locators_2` | `project_locators` | kind,normalized_value | 4,096 | UNIQUE |  | 1 | 1 |  |
| `sqlite_autoindex_projects_1` | `projects` | id | 4,096 | PK |  | 17 | 17 |  |
| `idx_relations_from_kind` | `relations` | work_id_from,kind,work_id_to | 241,664 | explicit | yes | 6 | 8 |  |
| `idx_relations_to_kind` | `relations` | work_id_to,kind,work_id_from | 241,664 | explicit | yes | 7 | 9 |  |
| `sqlite_autoindex_relations_1` | `relations` | work_id_from,work_id_to,kind | 270,336 | UNIQUE | yes | 0 | 0 |  |
| `sqlite_autoindex_runtime_state_writers_1` | `runtime_state_writers` | surface,definition_ref,knowledge_projection_version,digest | 4,096 | PK |  | 0 | 0 |  |
| `sqlite_autoindex_store_sequences_1` | `store_sequences` | name | 4,096 | PK |  | 1 | 1 |  |
| `sqlite_autoindex_work_items_1` | `work_items` | id | 98,304 | PK |  | 53 | 53 |  |
| `sqlite_autoindex_work_kinds_1` | `work_kinds` | kind | 4,096 | PK |  | 0 | 0 |  |
| `sqlite_autoindex_work_projects_1` | `work_projects` | work_id,project_id | 139,264 | PK | yes | 14 | 16 |  |
| `work_projects_by_project` | `work_projects` | project_id,work_id | 143,360 | explicit | yes | 4 | 5 |  |
| `work_projects_one_primary` | `work_projects` | work_id | 98,304 | explicit, partial | yes | 7 | 7 |  |
| `sqlite_autoindex_work_removal_operations_1` | `work_removal_operations` | operation_id | 4,096 | PK |  | 4 | 4 |  |
| `sqlite_autoindex_work_removal_operations_2` | `work_removal_operations` | idempotency_key | 4,096 | UNIQUE |  | 1 | 1 |  |
| `sqlite_autoindex_work_removal_operations_3` | `work_removal_operations` | work_id | 4,096 | UNIQUE |  | 4 | 4 |  |
| `work_removal_operations_work` | `work_removal_operations` | work_id | 4,096 | explicit |  | 0 | 0 | prefix-redundant (R13) |
| `sqlite_autoindex_work_version_actions_1` | `work_version_actions` | work_id,resulting_version,action_id | 2,158,592 | PK | yes | 2 | 4 |  |
| `sqlite_autoindex_workflow_actors_1` | `workflow_actors` | actor_ref | 1,142,784 | PK |  | 9 | 9 |  |
| `sqlite_autoindex_workflow_actors_2` | `workflow_actors` | principal_ref,client_ref,agent_ref,session_ref | 2,940,928 | UNIQUE |  | 0 | 0 |  |
| `sqlite_autoindex_workflow_architecture_bindings_1` | `workflow_architecture_bindings` | work_id,contract_version | 86,016 | PK | yes | 5 | 6 |  |
| `sqlite_autoindex_workflow_architecture_bindings_2` | `workflow_architecture_bindings` | work_id,contract_version,product_id | 110,592 | UNIQUE | yes | 0 | 1 |  |
| `sqlite_autoindex_workflow_backlog_alignment_1` | `workflow_backlog_alignment` | work_id,related_work_id | 135,168 | PK | yes | 2 | 4 |  |
| `workflow_backlog_alignment_related` | `workflow_backlog_alignment` | related_work_id | 65,536 | explicit | yes | 1 | 3 |  |
| `sqlite_autoindex_workflow_checkpoints_1` | `workflow_checkpoints` | work_id,checkpoint_id | 4,096 | PK | yes | 0 | 0 |  |
| `sqlite_autoindex_workflow_checkpoints_2` | `workflow_checkpoints` | work_id,step_id,attempt_epoch | 4,096 | UNIQUE | yes | 2 | 2 |  |
| `sqlite_autoindex_workflow_checkpoints_3` | `workflow_checkpoints` | work_id,idempotency_identity | 4,096 | UNIQUE | yes | 2 | 4 |  |
| `sqlite_autoindex_workflow_clarify_records_1` | `workflow_clarify_records` | work_id | 4,096 | PK | yes | 3 | 5 |  |
| `sqlite_autoindex_workflow_completed_exits_1` | `workflow_completed_exits` | work_id,definition_ref,step_id,action_id | 4,096 | PK | yes | 3 | 5 |  |
| `sqlite_autoindex_workflow_contract_affected_domains_1` | `workflow_contract_affected_domains` | work_id,contract_version,domain_id | 253,952 | PK | yes | 2 | 3 |  |
| `workflow_contract_affected_domains_lookup` | `workflow_contract_affected_domains` | domain_id,work_id,contract_version | 253,952 | explicit |  | 0 | 0 |  |
| `sqlite_autoindex_workflow_contract_domain_modifications_1` | `workflow_contract_domain_modifications` | work_id,contract_version,domain_id | 53,248 | PK | yes | 2 | 3 |  |
| `sqlite_autoindex_workflow_contract_domain_relation_modifications_1` | `workflow_contract_domain_relation_modifications` | work_id,contract_version,source_domain_id,kind,target_domain_id | 4,096 | PK | yes | 2 | 3 |  |
| `sqlite_autoindex_workflow_contract_law_additions_1` | `workflow_contract_law_additions` | work_id,contract_version,law_id | 12,288 | PK | yes | 3 | 4 |  |
| `sqlite_autoindex_workflow_contract_law_modifications_1` | `workflow_contract_law_modifications` | work_id,contract_version,law_id | 81,920 | PK | yes | 1 | 2 |  |
| `workflow_contract_law_modifications_lookup` | `workflow_contract_law_modifications` | law_id,work_id,contract_version | 69,632 | explicit |  | 0 | 0 |  |
| `sqlite_autoindex_workflow_contract_verification_obligations_1` | `workflow_contract_verification_obligations` | work_id,contract_version,law_id,obligation_id | 12,288 | PK | yes | 2 | 3 |  |
| `sqlite_autoindex_workflow_contracts_1` | `workflow_contracts` | work_id,contract_version | 90,112 | PK | yes | 13 | 16 |  |
| `sqlite_autoindex_workflow_coordinator_actions_1` | `workflow_coordinator_actions` | work_id,actor_ref | 4,096 | PK | yes | 2 | 4 |  |
| `sqlite_autoindex_workflow_decision_records_1` | `workflow_decision_records` | work_id,question | 4,096 | PK | yes | 2 | 4 |  |
| `sqlite_autoindex_workflow_delivery_state_1` | `workflow_delivery_state` | work_id | 32,768 | PK | yes | 5 | 7 |  |
| `sqlite_autoindex_workflow_design_records_1` | `workflow_design_records` | work_id,work_version | 32,768 | PK | yes | 0 | 0 |  |
| `workflow_design_records_latest` | `workflow_design_records` | work_id,work_version | 32,768 | explicit | yes | 1 | 3 | prefix-redundant (R13) |
| `sqlite_autoindex_workflow_evidence_bindings_1` | `workflow_evidence_bindings` | binding_id | 684,032 | UNIQUE |  | 0 | 0 |  |
| `sqlite_autoindex_workflow_evidence_bindings_2` | `workflow_evidence_bindings` | work_id,binding_seq | 483,328 | PK | yes | 1 | 1 |  |
| `workflow_evidence_bindings_subject` | `workflow_evidence_bindings` | work_id,(expression) | 1,134,592 | explicit | yes | 2 | 4 |  |
| `sqlite_autoindex_workflow_impact_edges_1` | `workflow_impact_edges` | work_id,edge_id | 4,096 | PK | yes | 3 | 5 |  |
| `workflow_impact_edges_target` | `workflow_impact_edges` | target_work_id,edge_kind,edge_class | 4,096 | explicit | yes | 2 | 4 |  |
| `sqlite_autoindex_workflow_instances_1` | `workflow_instances` | work_id | 98,304 | PK | yes | 25 | 27 |  |
| `workflow_instances_state` | `workflow_instances` | instance_state,work_id | 131,072 | explicit |  | 2 | 2 |  |
| `sqlite_autoindex_workflow_law_addition_reservations_1` | `workflow_law_addition_reservations` | product_id,law_id | 4,096 | PK | yes | 1 | 1 |  |
| `sqlite_autoindex_workflow_law_addition_reservations_2` | `workflow_law_addition_reservations` | product_id,law_id,owner_work_id,owner_contract_version,home_domain_id | 12,288 | UNIQUE | yes | 1 | 1 |  |
| `workflow_law_addition_reservations_owner` | `workflow_law_addition_reservations` | product_id,owner_work_id,owner_contract_version | 4,096 | explicit | yes | 0 | 4 |  |
| `sqlite_autoindex_workflow_native_run_statuses_1` | `workflow_native_run_statuses` | phase,status | 4,096 | PK |  | 0 | 0 |  |
| `sqlite_autoindex_workflow_native_runs_1` | `workflow_native_runs` | work_id,run_id,phase | 4,096 | PK | yes | 1 | 1 |  |
| `workflow_native_runs_work` | `workflow_native_runs` | work_id,run_id | 4,096 | explicit | yes | 2 | 4 | prefix-redundant (R13) |
| `sqlite_autoindex_workflow_overlap_resolutions_1` | `workflow_overlap_resolutions` | resolution_id | 319,488 | PK |  | 0 | 0 |  |
| `sqlite_autoindex_workflow_overlap_resolutions_2` | `workflow_overlap_resolutions` | event_seq | 40,960 | UNIQUE | yes | 0 | 0 |  |
| `workflow_overlap_resolutions_pair` | `workflow_overlap_resolutions` | product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,event_seq | 303,104 | explicit | yes | 0 | 0 |  |
| `workflow_overlap_resolutions_reverse_pair` | `workflow_overlap_resolutions` | product_id,to_work_id,from_work_id,to_contract_version,from_contract_version,event_seq | 294,912 | explicit | yes | 0 | 5 |  |
| `sqlite_autoindex_workflow_phase_exits_1` | `workflow_phase_exits` | work_id,action_id | 4,096 | PK | yes | 2 | 4 |  |
| `sqlite_autoindex_workflow_plan_records_1` | `workflow_plan_records` | work_id | 12,288 | PK | yes | 4 | 6 |  |
| `sqlite_autoindex_workflow_premise_confirmations_1` | `workflow_premise_confirmations` | work_id,contract_version | 53,248 | PK | yes | 4 | 5 |  |
| `sqlite_autoindex_workflow_proposal_records_1` | `workflow_proposal_records` | work_id,work_version | 28,672 | PK | yes | 0 | 0 |  |
| `workflow_proposal_records_latest` | `workflow_proposal_records` | work_id,work_version | 28,672 | explicit | yes | 1 | 3 | prefix-redundant (R13) |
| `sqlite_autoindex_workflow_removal_scopes_1` | `workflow_removal_scopes` | work_id | 4,096 | PK | yes | 2 | 4 |  |
| `sqlite_autoindex_workflow_step_epochs_1` | `workflow_step_epochs` | work_id,step_id | 4,096 | PK | yes | 5 | 7 |  |

Totals: 110 indexes (38 explicit, 72 automatic); 36,044,800 B. Zero hits: 31 in the earlier set and 28 in the FK-on set.

## Limitations

- **Growth.** Growth figures count surviving rows by a timestamp column whose insertion meaning is taken from its name. They show the net surviving population, not an insert rate.
- **JSON in Go.** How Go callers use JSON fields after decoding was not traced. Decode detection covers only the reading function.
- **Native context route.** Whether it surfaces retained facts (archived history, decision records, delivery links) was not established.
- **Q7.** Two Q7 plan shapes have no extracted plan: DESC order without a cursor, and ASC order with a cursor.
- **Trigger cost.** Stated from static plans and opcode counts only; no trigger timing was measured.


## Appendix J — Hot-route EXPLAIN QUERY PLAN output
These are the full EQP detail lines for the extracted statement variants in the route table and shared/absence paths, using modernc SQLite 3.54.1 with `foreign_keys` on against the immutable backup. IDs identify extracted statements, not executed invocation counts. Parameters and dynamic filters use the extracted variants; plan shapes may change with other inputs. Source paths identify each statement.

**14 — `internal/store/agent_authority.go:204`**

```text
SEARCH c USING INDEX sqlite_autoindex_agent_clients_1 (client_ref=?)
SEARCH k USING INDEX agent_client_one_active_key (client_ref=?) LEFT-JOIN
```
**15 — `internal/store/agent_mutation_boundary.go:62`**

```text
SEARCH idempotency_records USING INDEX sqlite_autoindex_idempotency_records_1 (principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?)
```
**19 — `internal/store/agent_mutation_boundary.go:115`**

```text
SEARCH idempotency_records USING INDEX sqlite_autoindex_idempotency_records_1 (principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?)
```
**90 — `internal/store/lifecycle.go:424`**

```text
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**137 — `internal/store/native_runs.go:200`**

```text
SEARCH workflow_native_runs USING INDEX sqlite_autoindex_workflow_native_runs_1 (work_id=?)
```
**170 — `internal/store/operation_result.go:40`**

```text
SEARCH durable_operations USING INDEX sqlite_autoindex_durable_operations_1 (op_id=?)
```
**173 — `internal/store/operator.go:74`**

```text
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**175 — `internal/store/planning_retirement.go:28`**

```text
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**193 — `internal/store/project_handoffs.go:634`**

```text
SEARCH project_handoffs USING INDEX sqlite_autoindex_project_handoffs_1 (handoff_id=?)
```
**207 — `internal/store/project_handoffs.go:1048`**

```text
SEARCH project_handoffs USING INDEX project_handoffs_recorded_seq (work_id=?)
```
**208 — `internal/store/project_handoffs.go:1071`**

```text
SEARCH project_handoffs USING INDEX project_handoffs_recorded_seq (work_id=?)
```
**209 — `internal/store/project_locator.go:112`**

```text
SEARCH product_projects USING INDEX sqlite_autoindex_product_projects_1 (product_id=?)
LIST SUBQUERY 1
SEARCH product_projects USING COVERING INDEX product_projects_by_project (project_id=?)
```
**213 — `internal/store/project_locator.go:439`**

```text
SEARCH projects USING INDEX sqlite_autoindex_projects_1 (id=?)
```
**219 — `internal/store/project_locator.go:506`**

```text
SEARCH project_locators USING INDEX sqlite_autoindex_project_locators_2 (kind=? AND normalized_value=?)
```
**232 — `internal/store/q7_current.go:117`**

```text
CO-ROUTINE entries
COMPOUND QUERY
LEFT-MOST SUBQUERY
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
LIST SUBQUERY 8
SCAN json_each VIRTUAL TABLE INDEX 1:
CREATE BLOOM FILTER
UNION ALL
MULTI-INDEX OR
INDEX 1
SCALAR SUBQUERY 3
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH r USING INDEX idx_relations_from_kind (work_id_from=?)
INDEX 2
SCALAR SUBQUERY 4
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH r USING INDEX idx_relations_to_kind (work_id_to=?)
SCALAR SUBQUERY 3
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
SCALAR SUBQUERY 4
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
REUSE LIST SUBQUERY 8
SEARCH f USING INDEX effect_receipts_relation_seq (relation_seq=?)
UNION ALL
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
REUSE LIST SUBQUERY 8
SEARCH x USING INDEX sqlite_autoindex_workflow_phase_exits_1 (work_id=?)
UNION ALL
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
REUSE LIST SUBQUERY 8
SEARCH b USING INDEX workflow_evidence_bindings_subject (work_id=?)
SCAN entries
REUSE LIST SUBQUERY 8
USE TEMP B-TREE FOR ORDER BY
```
**233 — `internal/store/q7_current.go:117`**

```text
CO-ROUTINE entries
COMPOUND QUERY
LEFT-MOST SUBQUERY
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
LIST SUBQUERY 8
SCAN json_each VIRTUAL TABLE INDEX 1:
CREATE BLOOM FILTER
UNION ALL
MULTI-INDEX OR
INDEX 1
SCALAR SUBQUERY 3
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH r USING INDEX idx_relations_from_kind (work_id_from=?)
INDEX 2
SCALAR SUBQUERY 4
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH r USING INDEX idx_relations_to_kind (work_id_to=?)
SCALAR SUBQUERY 3
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
SCALAR SUBQUERY 4
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
REUSE LIST SUBQUERY 8
SEARCH f USING INDEX effect_receipts_relation_seq (relation_seq=?)
UNION ALL
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
REUSE LIST SUBQUERY 8
SEARCH x USING INDEX sqlite_autoindex_workflow_phase_exits_1 (work_id=?)
UNION ALL
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
REUSE LIST SUBQUERY 8
SEARCH b USING INDEX sqlite_autoindex_workflow_evidence_bindings_2 (work_id=? AND binding_seq<?)
SCAN entries
REUSE LIST SUBQUERY 8
USE TEMP B-TREE FOR ORDER BY
```
**235 — `internal/store/query.go:226`**

```text
SEARCH products USING INDEX sqlite_autoindex_products_1 (id=?)
```
**246 — `internal/store/query.go:584`**

```text
CO-ROUTINE scoped
SEARCH pp USING COVERING INDEX sqlite_autoindex_product_projects_1 (product_id=?)
SEARCH wp USING COVERING INDEX work_projects_by_project (project_id=?)
USE TEMP B-TREE FOR DISTINCT
SCAN s
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
USE TEMP B-TREE FOR ORDER BY
```
**247 — `internal/store/query.go:584`**

```text
CO-ROUTINE scoped
SEARCH pp USING COVERING INDEX sqlite_autoindex_product_projects_1 (product_id=?)
SEARCH wp USING COVERING INDEX work_projects_by_project (project_id=?)
USE TEMP B-TREE FOR DISTINCT
SCAN s
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
USE TEMP B-TREE FOR ORDER BY
```
**248 — `internal/store/query.go:584`**

```text
CO-ROUTINE scoped
SEARCH wp USING COVERING INDEX sqlite_autoindex_work_projects_1 (work_id=?)
SEARCH pp USING COVERING INDEX sqlite_autoindex_product_projects_1 (product_id=? AND project_id=?)
SCAN s
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH l EXISTS USING INDEX sqlite_autoindex_linear_issue_links_1 (work_id=?)
USE TEMP B-TREE FOR ORDER BY
```
**249 — `internal/store/query.go:584`**

```text
CO-ROUTINE scoped
SEARCH wp USING COVERING INDEX sqlite_autoindex_work_projects_1 (work_id=?)
SEARCH pp USING COVERING INDEX sqlite_autoindex_product_projects_1 (product_id=? AND project_id=?)
SCAN s
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH l EXISTS USING INDEX sqlite_autoindex_linear_issue_links_1 (work_id=?)
USE TEMP B-TREE FOR ORDER BY
```
**250 — `internal/store/query.go:584`**

```text
CO-ROUTINE scoped
SEARCH pp USING COVERING INDEX sqlite_autoindex_product_projects_1 (product_id=? AND project_id=?)
SEARCH wp USING COVERING INDEX work_projects_by_project (project_id=?)
SCAN s
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
USE TEMP B-TREE FOR ORDER BY
```
**251 — `internal/store/query.go:584`**

```text
CO-ROUTINE scoped
SEARCH pp USING COVERING INDEX sqlite_autoindex_product_projects_1 (product_id=? AND project_id=?)
SEARCH wp USING COVERING INDEX work_projects_by_project (project_id=?)
SCAN s
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
USE TEMP B-TREE FOR ORDER BY
```
**252 — `internal/store/query.go:584`**

```text
CO-ROUTINE scoped
SEARCH wp USING COVERING INDEX sqlite_autoindex_work_projects_1 (work_id=? AND project_id=?)
SEARCH pp USING COVERING INDEX sqlite_autoindex_product_projects_1 (product_id=? AND project_id=?)
SCAN s
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH l EXISTS USING INDEX sqlite_autoindex_linear_issue_links_1 (work_id=?)
USE TEMP B-TREE FOR ORDER BY
```
**253 — `internal/store/query.go:584`**

```text
CO-ROUTINE scoped
SEARCH wp USING COVERING INDEX sqlite_autoindex_work_projects_1 (work_id=? AND project_id=?)
SEARCH pp USING COVERING INDEX sqlite_autoindex_product_projects_1 (product_id=? AND project_id=?)
SCAN s
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH l EXISTS USING INDEX sqlite_autoindex_linear_issue_links_1 (work_id=?)
USE TEMP B-TREE FOR ORDER BY
```
**255 — `internal/store/query.go:735`**

```text
SEARCH linear_issue_links USING INDEX sqlite_autoindex_linear_issue_links_1 (work_id=?)
```
**256 — `internal/store/query.go:772`**

```text
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**260 — `internal/store/query.go:849`**

```text
SEARCH w USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
CORRELATED SCALAR SUBQUERY 1
SEARCH r USING COVERING INDEX idx_relations_to_kind (work_id_to=? AND kind=?)
SEARCH b USING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**261 — `internal/store/query.go:891`**

```text
SEARCH wp USING INDEX sqlite_autoindex_work_projects_1 (work_id=?)
SEARCH p USING INDEX sqlite_autoindex_projects_1 (id=?)
USE TEMP B-TREE FOR ORDER BY
```
**2443 — `internal/store/scope.go:28`**

```text
SCAN archived_work
```
**2444 — `internal/store/scope.go:43`**

```text
MERGE (UNION)
LEFT
SEARCH wp USING COVERING INDEX sqlite_autoindex_work_projects_1 (work_id=?)
SEARCH pp USING COVERING INDEX product_projects_by_project (project_id=?)
USE TEMP B-TREE FOR DISTINCT
USE TEMP B-TREE FOR LAST TERM OF ORDER BY
RIGHT
SCAN p USING INDEX archived_work_products_lookup
SEARCH a USING INDEX sqlite_autoindex_archived_work_1 (home_project_id=? AND home_locator_id=? AND id=?)
CORRELATED SCALAR SUBQUERY 2
SEARCH w USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**2446 — `internal/store/scope.go:91`**

```text
SCAN CONSTANT ROW
SCALAR SUBQUERY 1
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**2449 — `internal/store/session_resume_read.go:25`**

```text
SEARCH wp USING COVERING INDEX sqlite_autoindex_work_projects_1 (work_id=?)
SEARCH pp USING COVERING INDEX product_projects_by_project (project_id=?)
USE TEMP B-TREE FOR DISTINCT
```
**2450 — `internal/store/session_resume_read.go:90`**

```text
SCAN CONSTANT ROW
SCALAR SUBQUERY 1
SEARCH workflow_instances USING COVERING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
```
**2452 — `internal/store/terminal_absence.go:10`**

```text
SCAN CONSTANT ROW
SCALAR SUBQUERY 1
SEARCH work_items USING COVERING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**2453 — `internal/store/terminal_absence.go:30`**

```text
SCAN a USING INDEX sqlite_autoindex_archived_work_1
CORRELATED SCALAR SUBQUERY 1
SEARCH s USING COVERING INDEX sqlite_autoindex_archived_work_products_1 (home_project_id=? AND home_locator_id=? AND work_id=? AND product_id=?)
CORRELATED SCALAR SUBQUERY 2
SEARCH p USING COVERING INDEX sqlite_autoindex_archived_work_projects_1 (home_project_id=? AND home_locator_id=? AND work_id=? AND project_id=?)
USE TEMP B-TREE FOR ORDER BY
```
**2454 — `internal/store/terminal_absence.go:58`**

```text
SCAN CONSTANT ROW
SCALAR SUBQUERY 1
SEARCH linear_issue_links USING COVERING INDEX sqlite_autoindex_linear_issue_links_1 (work_id=?)
```
**2455 — `internal/store/terminal_absence.go:84`**

```text
SCAN CONSTANT ROW
SCALAR SUBQUERY 1
SCAN archived_work
SCALAR SUBQUERY 5
SCAN a
CORRELATED SCALAR SUBQUERY 2
SEARCH p USING COVERING INDEX sqlite_autoindex_archived_work_projects_1 (home_project_id=? AND home_locator_id=? AND work_id=? AND project_id=?)
CORRELATED SCALAR SUBQUERY 3
SEARCH s USING COVERING INDEX sqlite_autoindex_archived_work_products_1 (home_project_id=? AND home_locator_id=? AND work_id=? AND product_id=?)
SCALAR SUBQUERY 4
SEARCH pp USING COVERING INDEX sqlite_autoindex_product_projects_1 (product_id=? AND project_id=?)
```
**2524 — `internal/store/work_removal.go:519`**

```text
SEARCH workflow_decision_records USING COVERING INDEX sqlite_autoindex_workflow_decision_records_1 (work_id=?)
```
**2525 — `internal/store/work_removal.go:523`**

```text
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**2534 — `internal/store/workflow.go:457`**

```text
SEARCH workflow_actors USING INDEX sqlite_autoindex_workflow_actors_1 (actor_ref=?)
```
**2536 — `internal/store/workflow.go:554`**

```text
SEARCH workflow_contracts USING INDEX sqlite_autoindex_workflow_contracts_1 (work_id=?)
```
**2545 — `internal/store/workflow.go:776`**

```text
SEARCH workflow_actors USING INDEX sqlite_autoindex_workflow_actors_1 (actor_ref=?)
```
**2550 — `internal/store/workflow.go:972`**

```text
SEARCH workflow_checkpoints USING COVERING INDEX sqlite_autoindex_workflow_checkpoints_2 (work_id=? AND step_id=?)
```
**2562 — `internal/store/workflow.go:1305`**

```text
SEARCH workflow_step_epochs USING INDEX sqlite_autoindex_workflow_step_epochs_1 (work_id=? AND step_id=?)
```
**2569 — `internal/store/workflow.go:1388`**

```text
SEARCH durable_operations USING INDEX sqlite_autoindex_durable_operations_1 (op_id=?)
```
**2575 — `internal/store/workflow.go:1418`**

```text
SEARCH durable_operations USING INDEX sqlite_autoindex_durable_operations_1 (op_id=?)
```
**2576 — `internal/store/workflow.go:1425`**

```text
SEARCH durable_operations USING INDEX sqlite_autoindex_durable_operations_1 (op_id=?)
SCAN json_each EXISTS VIRTUAL TABLE INDEX 1:
```
**2588 — `internal/store/workflow_action_guards.go:52`**

```text
SEARCH workflow_actors USING INDEX sqlite_autoindex_workflow_actors_1 (actor_ref=?)
```
**2590 — `internal/store/workflow_action_guards.go:116`**

```text
SEARCH durable_operations USING INDEX sqlite_autoindex_durable_operations_1 (op_id=?)
```
**2592 — `internal/store/workflow_action_guards.go:235`**

```text
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**2593 — `internal/store/workflow_action_guards.go:240`**

```text
SEARCH durable_operations USING INDEX sqlite_autoindex_durable_operations_1 (op_id=?)
```
**2617 — `internal/store/workflow_completion.go:62`**

```text
SCAN CONSTANT ROW
SCALAR SUBQUERY 1
SEARCH workflow_premise_confirmations USING COVERING INDEX sqlite_autoindex_workflow_premise_confirmations_1 (work_id=? AND contract_version=?)
```
**2618 — `internal/store/workflow_completion.go:72`**

```text
SCAN CONSTANT ROW
SCALAR SUBQUERY 1
SEARCH workflow_delivery_state USING COVERING INDEX sqlite_autoindex_workflow_delivery_state_1 (work_id=?)
```
**2619 — `internal/store/workflow_completion.go:122`**

```text
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**2620 — `internal/store/workflow_completion.go:144`**

```text
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**2621 — `internal/store/workflow_contract_authority.go:14`**

```text
SEARCH workflow_contracts USING INDEX sqlite_autoindex_workflow_contracts_1 (work_id=?)
```
**2623 — `internal/store/workflow_contract_authority.go:24`**

```text
SEARCH workflow_contracts USING INDEX sqlite_autoindex_workflow_contracts_1 (work_id=?)
```
**2638 — `internal/store/workflow_dispatch.go:92`**

```text
SEARCH durable_operations USING INDEX sqlite_autoindex_durable_operations_1 (op_id=?)
```
**2639 — `internal/store/workflow_dispatch.go:224`**

```text
SEARCH workflow_instances USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
CORRELATED SCALAR SUBQUERY 1
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
CORRELATED SCALAR SUBQUERY 2
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**2640 — `internal/store/workflow_dispatch.go:354`**

```text
SEARCH workflow_evidence_bindings USING COVERING INDEX workflow_evidence_bindings_subject (work_id=?)
```
**2641 — `internal/store/workflow_dispatch.go:359`**

```text
SEARCH workflow_premise_confirmations USING COVERING INDEX sqlite_autoindex_workflow_premise_confirmations_1 (work_id=? AND contract_version=?)
```
**2686 — `internal/store/workflow_plan_storage.go:15`**

```text
SEARCH workflow_instances USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
```
**2695 — `internal/store/workflow_read.go:29`**

```text
SEARCH workflow_contracts USING INDEX sqlite_autoindex_workflow_contracts_1 (work_id=? AND contract_version=?)
```
**2697 — `internal/store/workflow_records.go:17`**

```text
MERGE (UNION)
LEFT
MERGE (UNION)
LEFT
SEARCH workflow_completed_exits USING COVERING INDEX sqlite_autoindex_workflow_completed_exits_1 (work_id=?)
USE TEMP B-TREE FOR ORDER BY
RIGHT
SEARCH workflow_delivery_state USING COVERING INDEX sqlite_autoindex_workflow_delivery_state_1 (work_id=?)
RIGHT
MERGE (UNION)
LEFT
MERGE (UNION)
LEFT
SEARCH workflow_plan_records USING COVERING INDEX sqlite_autoindex_workflow_plan_records_1 (work_id=?)
RIGHT
SEARCH workflow_instances USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
RIGHT
SEARCH workflow_clarify_records USING COVERING INDEX sqlite_autoindex_workflow_clarify_records_1 (work_id=?)
```
**2698 — `internal/store/workflow_resume_read.go:60`**

```text
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH i USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
```
**2699 — `internal/store/workflow_resume_read.go:117`**

```text
SEARCH workflow_instances USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
```
**2704 — `internal/store/workflow_takeover.go:66`**

```text
SCAN CONSTANT ROW
SCALAR SUBQUERY 1
SEARCH workflow_step_epochs USING INDEX sqlite_autoindex_workflow_step_epochs_1 (work_id=?)
```
**2710 — `internal/store/workpin.go:82`**

```text
SEARCH work_items USING INDEX sqlite_autoindex_work_items_1 (id=?)
```
**2712 — `internal/store/workpin.go:104`**

```text
SCAN CONSTANT ROW
SCALAR SUBQUERY 1
SEARCH workflow_instances USING COVERING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
```
**2713 — `internal/store/workpin.go:145`**

```text
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH l USING INDEX sqlite_autoindex_linear_issue_links_1 (work_id=?) LEFT-JOIN
SEARCH wp USING INDEX work_projects_one_primary (work_id=?) LEFT-JOIN
SEARCH p USING INDEX sqlite_autoindex_projects_1 (id=?) LEFT-JOIN
```
**2714 — `internal/store/workpin.go:152`**

```text
SEARCH wi USING COVERING INDEX workflow_instances_state (instance_state=?)
SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)
SEARCH wp USING COVERING INDEX sqlite_autoindex_work_projects_1 (work_id=? AND project_id=?)
```
**2715 — `internal/store/workpin.go:171`**

```text
SEARCH workflow_instances USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
```
**2716 — `internal/store/workpin.go:282,294-308`**

```text
MULTI-INDEX OR
INDEX 1
SCALAR SUBQUERY 1
SEARCH workflow_instances USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
SEARCH a USING INDEX sqlite_autoindex_workflow_actors_1 (actor_ref=?)
INDEX 2
LIST SUBQUERY 2
SEARCH workflow_coordinator_actions USING COVERING INDEX sqlite_autoindex_workflow_coordinator_actions_1 (work_id=?)
SEARCH a USING INDEX sqlite_autoindex_workflow_actors_1 (actor_ref=?)
SCALAR SUBQUERY 1
SEARCH workflow_instances USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)
REUSE LIST SUBQUERY 2
SEARCH f USING INDEX sqlite_autoindex_workflow_coordinator_actions_1 (work_id=? AND actor_ref=?) LEFT-JOIN
USE TEMP B-TREE FOR ORDER BY
```
**2756 — `internal/store/internal/sqlitedb/revision.go:11`**

```text
SEARCH store_revision USING INTEGER PRIMARY KEY (rowid=?)
```

## Appendix K — Example statement site for each index with a plan hit

A site is one extracted runtime production statement whose plan selected the index. FK-on examples can be enforcement probes rather than explicit query access. The hit counts and their limits are in Appendix I.

| Index | Statement site | Plan set |
|---|---|---|
| `agent_client_one_active_key` | 9: `internal/store/agent_authority.go:125` | earlier |
| `sqlite_autoindex_agent_clients_1` | 3: `internal/store/agent_authority.go:62` | earlier |
| `sqlite_autoindex_agent_installation_keys_1` | 2752: `internal/store/internal/sqlitedb/installation_key.go:20` | earlier |
| `sqlite_autoindex_archived_work_1` | 69: `internal/store/law_boundary.go:755` | earlier |
| `archived_work_products_lookup` | 2444: `internal/store/scope.go:43` | earlier |
| `sqlite_autoindex_archived_work_products_1` | 2453: `internal/store/terminal_absence.go:30` | earlier |
| `sqlite_autoindex_archived_work_projects_1` | 2453: `internal/store/terminal_absence.go:30` | earlier |
| `domain_observations_open` | 35: `internal/store/domain_observations.go:195` | earlier |
| `sqlite_autoindex_durable_operations_1` | 170: `internal/store/operation_result.go:40` | earlier |
| `effect_receipts_relation_seq` | 232: `internal/store/q7_current.go:117` | earlier |
| `effect_receipts_subject` | 206: `internal/store/project_handoffs.go:1032` | earlier |
| `sqlite_autoindex_effect_receipts_1` | 105: `internal/store/lifecycle.go:745` | earlier |
| `external_observations_work` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_external_observations_1` | 2579: `internal/store/workflow.go:1439` | earlier |
| `sqlite_autoindex_idempotency_records_1` | 15: `internal/store/agent_mutation_boundary.go:62` | earlier |
| `linear_issue_links_human_key` | 110: `internal/store/linear_issue_link.go:124` | earlier |
| `linear_issue_links_remote_uuid` | 109: `internal/store/linear_issue_link.go:123` | earlier |
| `sqlite_autoindex_linear_issue_links_1` | 106: `internal/store/linear_issue_link.go:30` | earlier |
| `sqlite_autoindex_product_knowledge_homes_2` | 60: `internal/store/knowledge_home_resolution.go:28` | earlier |
| `sqlite_autoindex_product_knowledge_homes_1` | 71: `internal/store/law_boundary.go:1217` | earlier |
| `product_projects_by_project` | 71: `internal/store/law_boundary.go:1217` | earlier |
| `product_projects_one_primary` | 119: `internal/store/membership.go:183` | earlier |
| `sqlite_autoindex_product_projects_1` | 117: `internal/store/membership.go:170` | earlier |
| `products_display_name_order` | 240: `internal/store/query.go:384` | earlier |
| `sqlite_autoindex_products_1` | 123: `internal/store/membership.go:199` | earlier |
| `project_handoffs_recorded_seq` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `project_handoffs_source` | 201: `internal/store/project_handoffs.go:927` | earlier |
| `sqlite_autoindex_project_handoffs_1` | 183: `internal/store/project_handoffs.go:200` | earlier |
| `project_locators_project_locator` | 160: `internal/store/operation.go:841` | FK-on |
| `project_locators_by_project` | 72: `internal/store/law_boundary.go:1246` | earlier |
| `sqlite_autoindex_project_locators_2` | 219: `internal/store/project_locator.go:506` | earlier |
| `sqlite_autoindex_project_locators_1` | 176: `internal/store/product_knowledge_home.go:41` | earlier |
| `sqlite_autoindex_projects_1` | 85: `internal/store/lifecycle.go:259` | earlier |
| `idx_relations_to_kind` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `idx_relations_from_kind` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_store_sequences_1` | 2749: `internal/store/internal/sqlitedb/effect.go:23` | earlier |
| `sqlite_autoindex_work_items_1` | 20: `internal/store/agent_mutation_boundary.go:139` | earlier |
| `work_projects_by_project` | 131: `internal/store/membership.go:292` | earlier |
| `work_projects_one_primary` | 70: `internal/store/law_boundary.go:1207` | earlier |
| `sqlite_autoindex_work_projects_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_work_removal_operations_3` | 2480: `internal/store/work_removal.go:188` | earlier |
| `sqlite_autoindex_work_removal_operations_2` | 2480: `internal/store/work_removal.go:188` | earlier |
| `sqlite_autoindex_work_removal_operations_1` | 2479: `internal/store/work_removal.go:180` | earlier |
| `sqlite_autoindex_work_version_actions_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_actors_1` | 205: `internal/store/project_handoffs.go:986` | earlier |
| `sqlite_autoindex_workflow_architecture_bindings_2` | 154: `internal/store/operation.go:759` | FK-on |
| `sqlite_autoindex_workflow_architecture_bindings_1` | 55: `internal/store/knowledge_authority.go:281` | earlier |
| `workflow_backlog_alignment_related` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_backlog_alignment_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_checkpoints_3` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_checkpoints_2` | 2550: `internal/store/workflow.go:972` | earlier |
| `sqlite_autoindex_workflow_clarify_records_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_completed_exits_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_contract_affected_domains_1` | 2503: `internal/store/work_removal.go:440` | earlier |
| `sqlite_autoindex_workflow_contract_domain_modifications_1` | 2502: `internal/store/work_removal.go:440` | earlier |
| `sqlite_autoindex_workflow_contract_domain_relation_modifications_1` | 2501: `internal/store/work_removal.go:440` | earlier |
| `sqlite_autoindex_workflow_contract_law_additions_1` | 59: `internal/store/knowledge_authority.go:322` | earlier |
| `sqlite_autoindex_workflow_contract_law_modifications_1` | 2498: `internal/store/work_removal.go:440` | earlier |
| `sqlite_autoindex_workflow_contract_verification_obligations_1` | 2499: `internal/store/work_removal.go:440` | earlier |
| `sqlite_autoindex_workflow_contracts_1` | 54: `internal/store/knowledge_authority.go:242` | earlier |
| `sqlite_autoindex_workflow_coordinator_actions_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_decision_records_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_delivery_state_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `workflow_design_records_latest` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `workflow_evidence_bindings_subject` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_evidence_bindings_2` | 233: `internal/store/q7_current.go:117` | earlier |
| `workflow_impact_edges_target` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_impact_edges_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `workflow_instances_state` | 2556: `internal/store/workflow.go:1164` | earlier |
| `sqlite_autoindex_workflow_instances_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `workflow_law_addition_reservations_owner` | 154: `internal/store/operation.go:759` | FK-on |
| `sqlite_autoindex_workflow_law_addition_reservations_2` | 31: `internal/store/cd_reservations.go:24` | earlier |
| `sqlite_autoindex_workflow_law_addition_reservations_1` | 2606: `internal/store/workflow_architecture_binding.go:578` | earlier |
| `workflow_native_runs_work` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_native_runs_1` | 137: `internal/store/native_runs.go:200` | earlier |
| `workflow_overlap_resolutions_reverse_pair` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_phase_exits_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_plan_records_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_premise_confirmations_1` | 2505: `internal/store/work_removal.go:440` | earlier |
| `workflow_proposal_records_latest` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_removal_scopes_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
| `sqlite_autoindex_workflow_step_epochs_1` | 82: `internal/store/lifecycle.go:145` | FK-on |
