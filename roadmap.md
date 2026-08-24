# Roadmap

> **Status:** Advisory sequence. Not law.
> **Authority:** GitHub Issues own the work. [`docs/priorities.md`](docs/priorities.md)
> owns the priority order this sequence applies. Where either disagrees with a
> line below, this file is wrong.
> **Derived from:** the 33 open issues at `e76d206`, 2026-08-23.

This file orders open work. It does not create work, grant authority, or record
a decision. An entry here is a recommendation about *when* to take an issue, and
nothing more.

Three inputs produce the order:

1. The six ranked priorities in [`docs/priorities.md`](docs/priorities.md).
2. Blocking edges stated in the issues themselves.
3. Cost of delay, where an issue gets worse or harder while it waits.

[`docs/floor-readiness.v1.json`](docs/floor-readiness.v1.json) is **not** an
input, because it names none of these issues. It carries 39 `satisfied` items
and 1 `out_of_scope` item, with no `outstanding` state and no `issue` field.
That silence is itself tracked, by #213.

## Blocking edges

These six edges are stated in the issues. Every other issue is independent.

- #400 blocks #405.
- #326 blocks #325, which blocks #324.
- #212 blocks #213.
- #319 blocks #295.
- #316 blocks #295.

## Sequence

### 1. Stop the recurrence, then unblock

1. **#402** — CD numbers collide between concurrent branches.
   Land first. The collision already fired: #405 reserved CD-0064 in a comment
   and #427 took CD-0064 for a different subject, undetected. Every further
   merge that allocates a CD number widens the window.
2. **#400** — shipped workflow definitions derive from three historical layers.
   Blocks #405. Work exists on `fix/400-workflow-definition-collapse`
   (`95cf46c`) with no remote ref and no open pull request.
3. **#405** — how the conformance corpus represents Product-changing workflows.
   The accepted direction requires the collapse and the migrated corpus to land
   together. Needs a CD number that step 1 can protect.

### 2. Law-coverage integrity

Priority 1. Law that points at nothing cannot govern.

4. **#326** — decide PM7's disposition: implement pruning, or amend the policy.
   A decision. One of the six records in #325 waits on the answer.
5. **#325** — every outstanding law-coverage record points to a closed issue.
   Six shards under `docs/knowledge/coverage/` point at closed #219 and #257.
6. **#324** — bind outstanding law-coverage records to a live owning issue.
   The validator that makes #325 unrepeatable. Land with or after #325, or the
   check fails on the records it is meant to guard.

### 3. Silent correctness

Priority 1 and 2. Each of these is wrong now, and none of them announce it.

7. **#376** — the DB twin omits the overlap half of its Tx twin.
   Two same-named law-revision staleness checks disagree, and nothing records
   that they are allowed to.
8. **#383** — `concord_work_trace.history` refuses pages whose events carry no
   reason. A legitimate read fails today, reproducible against the generated
   schema.
9. **#379** — durable event-log timestamps come from `time.Now()`.
   The set of un-injectable write sites grows while this waits. One was added
   at `cmd/concord/session.go:81`.

### 4. Session identity

Finish the surface #418 opened. Taking one without the other re-opens it.

10. **#428** — the asserted orchestrator identity is not the agent the session
    starts.
11. **#430** — a resolvable orchestrator definition is not proof the host
    registered it.

### 5. Halves that reach no caller

A capability that no surface can invoke is not delivered.

12. **#316** — worktree locator derivation has no owner, so `worktree_claim`
    has no caller. Blocks #295, which gives it the most downstream weight in
    this group.
13. **#424** — `ExternalObservationsForWork` has no production caller.
14. **#423** — work observations are readable only through the bounded
    continuity snapshot. Same class as #424; one design answer likely serves
    both.
15. **#246** — the agent surface cannot resolve external conditions at a
    consequential boundary. The resolver is passed `nil`, so the walk is
    undispatchable end to end.
16. **#253** — the lane worker pipeline reaches no installation.
    Narrowed by #425 and #389 to the spawn half alone.

### 6. Migration closure

Priority 1. #295 cannot close until the three below land.

17. **#319** — define and prove the knowledge formalization process.
    The prose half exists; the executable half does not. Blocks #295.
18. **#318** — nothing prevents predecessor tool dependence in Concord's own
    agent surface.
19. **#317** — trunk file-write enforcement ends with the predecessor and the
    coverage row overstates what remains.
20. **#295** — build the Advance-to-Concord Product migration path.
    Closes only when #316, #319, knowledge closure, shadow evaluation, and
    strength harvest all land.

### 7. Measurement honesty

21. **#212** — record a reproducible lane eval baseline bound to both digests.
22. **#213** — represent unmeasured lane sufficiency in floor readiness.
    Blocked by #212. Repairs the manifest silence named at the top of this file.
23. **#309** — the conformance latency gate flakes under high host load.
    A tax on every run.

### 8. Debt with no forcing function

Real, measured, and not urgent. Take these when the sections above are clear,
or when a change lands nearby anyway.

24. **#264** — relation id assignment issues one count query per relation during
    log rebuild. Bites at scale.
25. **#288** — `applyWorkflowActionRawTx` interleaves per-action guards with
    shared invariants. Regressed from 104 to 139 cognitive complexity, and
    [`docs/complexity-budget.v1.json`](docs/complexity-budget.v1.json) tracks it,
    so it is measured rather than hidden.
26. **#406** — `(runtime).mutate` is 819 lines. The issue states that nothing
    in it is urgent or blocking.
27. **#404** — `cmd/concord` and `internal/store` bound the race step.
    Narrow the issue body first: the in-package template helper it asks for
    already exists at `internal/store/test_fixture_test.go`.
28. **#388** — rename `component_ids` to `domain_ids` per CD-0041.
    Mechanical, spans 23 files, low risk.

### 9. Decisions

None of these move without an operator answer, and no code change settles any of
them. Batch them into one sitting; five separate sessions pay the context cost
five times.

29. **#387** — whether the Domain attachments read surface carries resource
    purpose and environments.
30. **#181** — whether CD-0002's durable-tier binding rules become executable
    law or stay documented intent.
31. **#179** — whether the TS1 `fixture_override` keys are honored, typed
    closed, or removed. Narrow the body first: the facility is load-bearing for
    AJ5 keys now, and inert only for the two AJ6 keys.
32. **#169** — whether the PM1 fixture must move to `work.memberships_replaced`.
33. **#46** — the episode product-scoping probe outcome and the
    promotion-receiving contract.

## Maintenance

This file states issue numbers and their order. It restates neither issue bodies
nor their acceptance criteria, because those live in the issues and a copy here
would drift with no check to catch it.

It sits at the repository root, outside `knowledge_roots`, so
`check-knowledge-closure.py` and `check-doc-contract.py` do not read it. No
validator will notice when it goes stale. Delete it when it stops being true.
