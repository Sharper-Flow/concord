# Archive Briefing Digest

**Change ID:** addOrchestratorContinuityHook
**Title:** Add orchestrator continuity hook
**Status:** archived
**Generated:** 2026-08-30T23:26:52.167Z

## Identity Anchors

- CHANGE
- STATUS
- TERMINAL_GATE_SUMMARY

## Archive Digest

**Status:** archived

| Gate | Status |
| --- | --- |
| proposal | done |
| discovery | done |
| design | done |
| planning | done |
| execution | done |
| acceptance | done |
| release | pending |

## Epic Context

No Epic membership

## Durable Facts

Showing 50 of 50 durable facts.

- **[report_follow_up]** follow_ups: Delete unregistered ~/.config/opencode/concord-adapter.ts leftover (environment cleanup, out of repo scope).
- **[report_follow_up]** follow_ups: Decide hook scoping for non-orchestrator sessions in the same host (sessionID/agent guard) — Advance's internal-call guard is the precedent.
- **[report_follow_up]** follow_ups: Product-only sessions (empty CONCORD_SELECTED_WORK_ID) must render no continuity block, matching session.go:179-191.
- **[research_citation]** sources: Host plugin typings (installed): experimental.chat.system.transform declared: (input {sessionID?, model}, output {system: string[]}). (~/.config/opencode/node_modules/@opencode-ai/plugin/dist/index.d.ts:265-270)
- **[research_citation]** sources: Advance plugin (production usage): Hook writes one ordered block to output.system[0]; per-turn semantics proven by 'next turn' manifest reread (1326-1327), per-turn byte tracking (1378), compaction re-emit logic (1394-1405). (~/dev/advance/plugin/src/index.ts:1305-1409)
- **[research_citation]** sources: Advance system-block assembler: Replace-not-append, sentinel-delimited single system entry per turn; internal-call guard precedent. (~/dev/advance/plugin/src/utils/system-block.ts:1-31)
- **[research_citation]** sources.omitted: 7 additional sources omitted (bounded to first 3)
- **[archive_only_evidence]** architecture_assessment: All six load-bearing mechanisms check out against source. The hook is real and per-turn; the sessionboot render is byte-deterministic; identity env vars provably reach the host process; StepActions needs zero new lookup machinery; the contract pipeline is closed and lockstep-enforced. Three corrections are mandatory before prep: (1) the install-reconciliation premise is factually wrong — concord-plugin.ts already ships in-repo, in installed v4.10.3, and is the live registered plugin entry (opencode.jsonc:53), so that work item dissolves and the hook lands in the existing entry module; the unregistered ~/.config/opencode/concord-adapter.ts is a leftover to delete, not the live entry; (2) 'available_actions per-step' is a schema misread — per-step data is steps[].actions (schema:55); available_actions is definition-level (schema:17); (3) 'surface bumps to 2.4.0' names no repo field — surface version exists only in CD prose (CD-0016:56, CD-0024:46); the machine identity is ManifestDigest + schema consts, and the change must state where 2.4.0 is recorded. CD-0031/CD-0016 amendments are required companion records, not blockers.
- **[archive_only_evidence]** decisions: Named the exported helper DeriveSessionBoot and routed session and project launch through it. — Go export names start with an uppercase letter, and both existing session transports need the shared helper.
- **[archive_only_evidence]** decisions: Kept continuity-block output as raw packet bytes without a newline. — The verb must print the exact deterministic sessionboot.Build output.
- **[archive_only_evidence]** decisions: Added a bootstrap injection seam for transport tests. — The tests can verify transport behavior against a stable fixture snapshot without adding store logic.
- **[archive_only_evidence]** verification: go test ./cmd/concord/ -run TestContinuityBlock -count=1 (1) — RED failed because the continuity-block command helper was not implemented.
- **[archive_only_evidence]** verification: go test ./cmd/concord/ -run TestContinuityBlock -count=1 (1) — GREEN retry found one stale deriveSessionBoot caller in project_launch.go.
- **[archive_only_evidence]** verification: go test ./cmd/concord/ -run TestContinuityBlock -count=1 (0) — GREEN focused continuity-block tests passed.
- **[archive_only_evidence]** verification: go test ./... -count=1 (0) — VERIFY full Go test suite passed.
- **[archive_only_evidence]** decisions: Projected actions from the verified pinned definition's matching StepGraph step into pinned.step_actions. — This uses the required registry Lookup plus Verify path and avoids the unrelated definition-wide available_actions list.
- **[archive_only_evidence]** decisions: Regenerated all outputs with scripts/generate-agent-contracts.py after editing contracts/agent-tool-surface-payloads.schema.json. — The session_boot_packet continuity property references the shared continuity_snapshot definition, so one schema source change updates both payload consumers in lockstep.
- **[archive_only_evidence]** decisions: Normalized nil StepActions to an empty slice in ContinuityPayload. — Closed payload validation requires an array, and absent workflow data must serialize as [] rather than null.
- **[archive_only_evidence]** verification: go test ./internal/store/ -run TestContinuityStepActions -count=1 (1) — RED failed at compile because StepActions did not exist.
- **[archive_only_evidence]** verification: go test ./internal/store/ -run TestContinuityStepActions -count=1 (0) — GREEN passed current-step action projection and empty non-nil action slice without a workflow.
- **[archive_only_evidence]** verification: go test ./... -count=1 (0) — Full Go test suite passed.
- **[archive_only_evidence]** verification: bun test adapter/opencode/*.test.ts (0) — All 100 adapter tests passed with 761 assertions.
- **[archive_only_evidence]** verification: python3 scripts/generate-agent-contracts.py --check && python3 scripts/test-agent-contracts.py (0) — Generated contract check passed and all 53 generator contract tests passed.
- **[archive_only_evidence]** decisions: Put hook logic in a new sibling module and reuse the dispatch runner and binary-path helpers. — This keeps concord.ts transport-only and uses the existing Bun.spawn pattern without duplicating it.
- **[archive_only_evidence]** decisions: Key the ten-second spawn gate by CONCORD_SELECTED_PRODUCT_ID and CONCORD_SELECTED_WORK_ID. — These are the launcher identity variables consumed by the existing adapter and core session command.
- **[archive_only_evidence]** verification: bun test adapter/opencode/continuity_hook.test.ts (1) — RED failed before the continuity-hook module existed.
- **[archive_only_evidence]** verification: bun test adapter/opencode/continuity_hook.test.ts (0) — GREEN passed 5 focused tests covering registration, determinism, replacement, no-op failures, absent identity, empty work, and TTL gating.
- **[archive_only_evidence]** verification: bun test adapter/opencode (0) — VERIFY passed 105 adapter tests with 778 expectations.
- **[unresolved_action]** required_main_agent_actions: Checkpoint the seven reviewer remediation files before acceptance completion.
- **[unresolved_action]** required_main_agent_actions: Integrate the two newer main commits, then rerun the Go suite, adapter suite, and validator chain before release.
- **[unresolved_action]** required_main_agent_actions: After deployment, run at least five unchanged-state orchestrator turns and then run cache-stats summary. Record the result before claiming live cache safety.
- **[unresolved_action]** required_main_agent_actions: Restart OpenCode to clear any stale host plugin module cache from the completed adapter cleanup task.
- **[archive_only_evidence]** changes_made: contracts/adapter-continuity-scenarios.schema.json: Changed the scenario contract constant from stale CD-0088 to CD-0090.
- **[archive_only_evidence]** changes_made: scenarios/adapter-continuity.v1.json: Changed the scenario contract reference from stale CD-0088 to CD-0090.
- **[archive_only_evidence]** changes_made: adapter/opencode/continuity_hook.test.ts: Added an assertion that the hook carries pending_messages from core output into the sentinel block.
- **[archive_only_evidence]** changes_made: cmd/concord/session_test.go: Added an explicit assertion that continuity-block output includes pending_messages.
- **[archive_only_evidence]** changes_made: docs/decisions/CD-0090-per-turn-continuity-re-pin-hook.md: Corrected first-insert versus replacement behavior, failure wording, stale live-measurement claim, and recorded the deferred cache procedure.
- **[archive_only_evidence]** changes_made: docs/knowledge/records/CD-0090.json: Updated the CD-0090 content hash after the decision-record correction.
- **[archive_only_evidence]** changes_made: docs/concord-knowledge-index.v1.json: Regenerated the knowledge index from the corrected CD-0090 record shard.
- **[archive_only_evidence]** verification: tests_run=bun test adapter/opencode/continuity_hook.test.ts, go test ./cmd/concord -run 'TestContinuityBlock' -count=1, go test ./... -count=1, bun test adapter/opencode, python3 scripts/check-json.py && python3 scripts/check-knowledge-index.py && python3 scripts/check-knowledge-closure.py && python3 scripts/check-law-coverage.py && python3 scripts/check-doc-links.py && python3 scripts/check-doc-contract.py && python3 scripts/check-cd-allocation.py && python3 scripts/check-floor-readiness.py, git diff --check $(git merge-base main HEAD), git diff --exit-code $(git merge-base main HEAD) -- adapter/opencode/concord.ts results=pass — AC1 PASS at scenarios/adapter-continuity.v1.json:48-53 and concord-plugin.ts:45. AC2 PASS at session.go:116-133 and continuity-hook.ts:40-58. AC3 PASS at continuity_hook.test.ts:35-89 and CD-0090:108-113; live cache measurement is explicitly deferred. AC4 PASS at continuity_hook.test.ts:93-140 and session_test.go:137-168. AC5 PASS at continuity-hook.ts:3,44-58 and continuity_hook.test.ts:142-154. AC6 PASS at runtime.go:1783-1790, session_test.go:107-133, and continuity_hook.test.ts:53-64. Go full suite passed. Adapter suite passed 106 tests and 779 assertions. All eight requested validators passed. No added secret, debug, TODO, FIXME, XXX, or HACK pattern was found. concord.ts had no diff.
- **[unresolved_action]** required_main_agent_actions: Aggregate this READY remediation report into the harden release decision.
- **[wisdom_candidate]** wisdom_candidates: [gotcha] New runtime modules imported by adapter entry files must appear in scripts/install.py ADAPTER_FILES. Packaging and contract checks derive their shipped file set from this tuple.
- **[archive_only_evidence]** changes_made: scripts/install.py: Added continuity-hook.ts to ADAPTER_FILES between credentials.ts and dispatch.ts, so installer and release packaging include the runtime import.
- **[archive_only_evidence]** verification: tests_run=python3 -c 'import sys; sys.path.insert(0, "scripts"); import install; assert "continuity-hook.ts" in install.ADAPTER_FILES', python3 scripts/test-installer.py, python3 scripts/check-agent-contracts.py results=pass — The targeted check failed before the fix and passed after it. Post-fix exit codes were 0, 0, and 0. Installer tests: 40 passed. Contract checks: 53 Python tests and 106 Bun tests passed. The shipped adapter bundle included 8 modules and passed syntax, build, and typecheck checks. Commit: 8247df4a044183a85e2280ea6a5dc1a1102f7eba.
- **[unresolved_action]** consumer_warnings: verification_missing: Reviewer aggregate evidence is non-authoritative; no typed adv_run_test run ID proves command: python3 -c 'import sys; sys.path.insert(0, "scripts"); import install; assert "continuity-hook.ts" in install.ADAPTER_FILES'
- **[unresolved_action]** consumer_warnings: verification_missing: Reviewer aggregate evidence is non-authoritative; no typed adv_run_test run ID proves command: python3 scripts/test-installer.py
- **[unresolved_action]** consumer_warnings: verification_missing: Reviewer aggregate evidence is non-authoritative; no typed adv_run_test run ID proves command: python3 scripts/check-agent-contracts.py
- **[report_follow_up]** follow_ups: FU-CACHE-MEASURE (non-blocking, from task tk-35726ae8cf0e, AC3): live cache-meter measurement on first real orchestrator session post-deploy — N>=5 turns with no state change, then cache-stats summary; pass = cache_creation equals baseline after turn 1. Mechanism already machine-proven by adapter determinism tests; procedure recorded on the task.
- **[archive_only_evidence]** findings: [issue] deployment-readiness: HARDEN-DEPLOY-1 (fixed): new adapter runtime module missing from installer packaging; an installed release would omit it and the plugin entry would fail to load.
- **[archive_only_evidence]** findings: [issue] release-collision: HARDEN-MERGE-1 (fixed): branch conflicted with same-day upstream launcher removal; resolved by rebase.

## Contract / AC Coverage

No contract items.

## Unresolved Actions

- Checkpoint the seven reviewer remediation files before acceptance completion.
- Integrate the two newer main commits, then rerun the Go suite, adapter suite, and validator chain before release.
- After deployment, run at least five unchanged-state orchestrator turns and then run cache-stats summary. Record the result before claiming live cache safety.
- Restart OpenCode to clear any stale host plugin module cache from the completed adapter cleanup task.
- Aggregate this READY remediation report into the harden release decision.
- verification_missing: Reviewer aggregate evidence is non-authoritative; no typed adv_run_test run ID proves command: python3 -c 'import sys; sys.path.insert(0, "scripts"); import install; assert "continuity-hook.ts" in install.ADAPTER_FILES'
- verification_missing: Reviewer aggregate evidence is non-authoritative; no typed adv_run_test run ID proves command: python3 scripts/test-installer.py
- verification_missing: Reviewer aggregate evidence is non-authoritative; no typed adv_run_test run ID proves command: python3 scripts/check-agent-contracts.py
