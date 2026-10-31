---
concord_work_id: work-8087fbfdd9566326d95e2121
work_type: implementation
title: Remove Domain Shape and the code-ownership map
completed_at: 2026-10-31T16:54:53Z
outcome_tag: shipped
lesson_tags: [subtraction, verified-snapshot]
terminal_state: completed
priority: 0
summary: Removed typed Domain facts and inferred code ownership while retaining Domain law organization and verified-snapshot upgrade safety.
product_ids: [concord]
project_ids: [concord]
domain_ids: [agent-contract, agent-evolution, operator-surface, product-memory, product-root:concord, repository-verification, workflow-engine]
tag_ids: []
---

# Domain law organization without Domain Shape

[CON-1511](https://linear.app/sharper-flow/issue/CON-1511) removed the parallel typed projection and package-map maintenance in [PR2204](https://github.com/Sharper-Flow/concord/pull/2204). The PR records the complete law and test-retirement inventory. Domains still organize law; they no longer infer code ownership.

## Delivered outcome

- Removed typed facts, extractors, checks, generated setup, ratchet, shape/footprint reads, the package map, their schemas and tool declarations, and the stewardship skill. No wrapper, replacement map or empty method remains.
- Retained Domain registry, list/detail, observations, law home/applicability and architecture bindings. Knowledge-home designation retains configuration, version, eligibility and contested-home behavior without filesystem scaffolding.
- Breaking migration 172 drops five derived tables: `knowledge_domain_facts`, `knowledge_domain_roots`, `knowledge_domain_shapes`, `knowledge_package_domains` and `law_evidence_paths`. Applied migrations and checksums remain immutable.
- The existing upgrade route verifies a pre-upgrade snapshot before any drop. Persisted `domain_shape` required readings refuse upgrade before destructive change. Live work can replace obsolete readings through the pre-retirement binary; immutable terminal declarations require existing verified published-compaction cleanup. Missing recovery evidence leaves the old store intact.
- Removed the footprint scenario and pinned the genuine corpus at 19. Regression cases accept 19 and reject 18 or 20. Declarative tooling checks remain; only the orphan runtime parser and its parser-only tests were removed.

## Verification and acceptance

Independent exact-head Review accepted candidate `8ceba8ac89af67bf0683caff5d5a0fa06ed23363`, tree `2e4ddb955b139f656c2b2d97b0d5723073321f6e`, with no open findings. The coordinator recorded Review as `workflow-1765f29ad6daecafee0fcca2:completed` and associated passing captured proof for all three contract criteria through `workflow-fdf2831aa029e833851fd827`.

Focused checks exercised absence of retired operations/storage, retained Domain and architecture behavior, configuration-only designation, all legacy-reading lifecycles, snapshot contents, failed-snapshot prevention and transactional rollback after a failed drop. Installer, adapter publication and generation checks passed. Prior-head full checks were not relabeled as exact-head runs; required PR CI supplied fresh full contract and host-typecheck evidence.

The first CI run exposed stale payload/CLI expectations and a designation transaction-scope violation. All four witnesses reproduced before correction and passed afterward. Current assertions and guards were retained. Independent Review corrected its earlier incomplete pass.

The installed pre-push hook passed. Required [PR CI](https://github.com/Sharper-Flow/concord/actions/runs/41192556892) passed on `8ceba8ac89af67bf0683caff5d5a0fa06ed23363`, and [merge-group CI](https://github.com/Sharper-Flow/concord/actions/runs/41192957092) passed on exact merge `1b6ac3b815940059a751452473237d732a209922`. Post-merge [CodeQL](https://github.com/Sharper-Flow/concord/actions/runs/41193418436) and the [automated release](https://github.com/Sharper-Flow/concord/actions/runs/41193419384) succeeded on that merge, publishing [v35.0.0](https://github.com/Sharper-Flow/concord/releases/tag/v35.0.0).

The installed pre-cutover runtime required captured proof for refinement. Exact declared argv `go vet ./...` passed on the unchanged reviewed candidate, receipt `worktree_verify:sha256:dd48eacf53438dd7853a6bb02ef8909117875a8cf9b77b6da56d5fd8c2cbf017`. The nine printed checks were a catalogue, not nine required executions. My contrary diagnosis was wrong; the [correction](https://linear.app/sharper-flow/issue/CON-1511#comment-353f8651-d51b-4bb1-af34-666af3f6be22) records the admitted recovery. No broad rerun, bypass or restored runner was needed.

## Law changes

These agent-enacted revisions are derived. Assigned Concord retirement uses CD-0243 D3 and changes no approval boundary.

- Retired CD-0244, `.concord/law/decisions/CD-0244-domains-own-code-exclusively-and-hold-typed-facts.md`, in full: exclusive roots, package map, facts, extractors, checks, setup, reads and ratchet. Its requirements retired with the unwanted mechanism.
- Amended [CD-0222](../../law/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md): removed map/fact/ratchet/footprint obligations while retaining registry-based law organization, exact law revisions and architecture binding. Integrated main's separate retirements without reversing them.
- Amended [CD-0245](../../law/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md): retired the stewardship instance and its checks; retained generic skill portability, admission, installation and release rules. No replacement framework was introduced.
- Amended [CD-0249](../../law/decisions/CD-0249-decisions-carry-no-coverage-records.md): removed fact-anchor/footprint-only wording and corrected the historical migration-proof citation. Current typed anchors, coverage retirement and reachability remain.
- Amended [priorities](../../law/constitution/priorities.md): removed typed-fact/map/footprint requirements while retaining affected-Domain declarations, exact-law declarations and approval boundaries.

Test deletions follow CD-0244 D2/D4-D7 and CD-0222 D2/D12 for map/facts/checks/read behavior; CD-0244 D3/D8 for generated setup and operator surfaces; and CD-0245 D1 for stewardship. CD-0192 D2/D3 already retired the runtime tooling-equality gate. Mixed tests retain current obligations; the PR maps each deleted test family to its retiring clause and replacement proof.

## Remaining boundaries

No release was installed and no live-store cutover was performed. Cutover remains separate host coordination; this note grants no installation, migration or approval waiver.

Knowledge-index retirement belongs to [CON-1515](https://linear.app/sharper-flow/issue/CON-1515); its owner integrates these shared projection deletions. Domain attachments, unrelated law reservations and repository dependencies remain outside this item.

Live-reading remediation is tested with modeled persisted state, not an end-to-end run of an older binary. Terminal recovery tests the actual settled-publication cleanup seam without relaxing its evidence gates.
