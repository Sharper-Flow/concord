# Executive Summary — addOrchestratorContinuityHook

## Value

Concord's continuity law (CD-0016) says a summary can never carry workflow
position or authority — yet after mid-session host compaction, with no
intervening `concord_*` call, nothing re-asserted the pinned projection, so
workflow position traveled by summary alone. That window is now closed: every
turn re-pins the orchestrator's durable continuity into the prompt, and the
current workflow step's available actions arrive with it (surface 2.4.0),
replacing agent-selected command contracts with state-derived instruction.
Verified by RED→GREEN→VERIFY test cycles on all three code tasks, an
independent acceptance review (PASS on all six criteria), and the repo's full
validator chain.

## Release Readiness Summary

| Row | Status | Evidence |
|---|---|---|
| Ops readiness | ready | harden compact pass: all six dimensions checked with evidence; scanner bundle `change:scanner-bundle:harden\|1` |
| Migration / data impact | n/a (source-backed) | no schema migration; payload additions regenerate through the existing contract pipeline; knowledge index and law coverage regenerated, validators exit 0 |
| Frontend / preview impact | n/a (source-backed) | no frontend surface in scope |
| Collision / release risk | clear | merge dry-run vs origin/main clean after rebase onto b09e1b5 (#631 launcher removal, #630 ignores); one packaging HIGH found and fixed (commit 8247df4, test-installer + contract checks exit 0) |
| Open follow-ups | 1 non-blocking | FU-CACHE-MEASURE: live cache-meter number on first real orchestrator session post-deploy; mechanism already machine-proven by determinism tests; procedure on task tk-35726ae8cf0e |
| Next action | `/adv-archive addOrchestratorContinuityHook` | harden status READY |

## Approval Consequence Context

If archived: branch `change/addOrchestratorContinuityHook` merges; CD-0090
becomes product law; agent-tool surface moves to 2.4.0 with `step_actions`;
CD-0016/CD-0031 recorded consequences carry amendment blockquotes. A restart
of OpenCode drops the removed unregistered leftover entry. The live
cache-meter measurement remains the one post-deploy verification owed.
