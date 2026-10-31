Four items (work-ffd855dd, work-13e6717f, work-e103255e, work-aad52b54) shipped code but left the record open. Each predecessor session concluded it was blocked. None was.

1. A supersede_contract refusal does not establish that every correction route is unavailable. Read the current step, the pinned contract, and the declared recovery intents. Ordinary stale-contract recovery and the complete-step correction route have separate admission rules (CD-0186; internal/store/workflow_dispatch.go; internal/store/workflow_action_guards.go).

2. Coordinator premise confirmation requires an investigation artifact. requireRecordedInvestigationArtifact (internal/store/workflow_confirmation_investigation.go) requires a work observation whose refs resolve to a current Product Domain and, when the Product holds another work item, a different work item. File paths and pull request URLs do not satisfy it.

3. confirm_premise requires the contract's evidence kinds and verification obligations to be bound (internal/store/workflow_completion.go, requireAcceptanceDeliverables). The action records the authenticated coordinator; it carries no operator selection or question digest.

4. A review lane refusing dispatch at an external-effect step is not a missing review route. docs/managed-development-authority.md gives pull requests plus required checks the review and merge authority. Bind the merged PR as review evidence.

5. The workflow complete action moves the lifecycle itself (CD-0183 D3). The completion fold appends the terminal work.transitioned event in the same transaction (internal/store/workflow_completion.go).

6. Verdict evaluation_evidence must name an already-bound immutable_subject_ref (internal/store/workflow_completion.go, verifyVerdictEvidence). Bind evidence at a step that admits bind_evidence. If an obligation remains unbound, inspect the declared recovery intents instead of assuming that the binding route is absent (internal/store/workpin.go).

General rule: when a refusal names a remedy, the remedy is usually real and cheap. Probe the live surface before concluding a restart or new code is needed. Three of these four sessions recommended a restart; none required one.
