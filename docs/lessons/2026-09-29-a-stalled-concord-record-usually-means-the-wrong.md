Four items (work-ffd855dd, work-13e6717f, work-e103255e, work-aad52b54) shipped code but left the record open. Each predecessor session concluded it was blocked. None was.

1. A supersede_contract refusal does not establish that every correction route is unavailable. Read the current step, the pinned contract, and the declared recovery intents. Ordinary stale-contract recovery and the complete-step correction route have separate admission rules (CD-0186; internal/store/workflow_dispatch.go; internal/store/workflow_action_guards.go). At a human checkpoint, the core can offer Revise as an operator decision.

2. The operator question exists only after an investigation artifact. requireRecordedInvestigationArtifact (internal/store/workflow_operator.go) admits a confirm_premise question only when a work observation's refs resolve to a current Product Domain AND a different work item. File paths and pull request URLs do not satisfy it. Without the artifact, pending_operator_decision is null and the adapter misdirects the caller to read it anyway. Record the observation with a Domain id and a peer work id first.

3. confirm_premise requires the contract's evidence kinds and verification obligations to be bound (internal/store/workflow_completion.go, requireAcceptanceDeliverables). Read the open question immediately before the call and pass its exact decision_context_digest. The core refuses a digest that does not match the current decision context (internal/store/workflow_operator.go).

4. A review lane refusing dispatch at an external-effect step is not a missing review route. docs/development-authority.md gives pull requests plus required checks the review and merge authority. Bind the merged PR as review evidence.

5. The workflow complete action moves the lifecycle itself (CD-0183 D3). The completion fold appends the terminal work.transitioned event in the same transaction (internal/store/workflow_completion.go).

6. Verdict evaluation_evidence must name an already-bound immutable_subject_ref (internal/store/workflow_completion.go, verifyVerdictEvidence). Bind evidence at a step that admits bind_evidence. If an obligation remains unbound, inspect the declared recovery intents instead of assuming that the binding route is absent (internal/store/workpin.go).

General rule: when a refusal names a remedy, the remedy is usually real and cheap. Probe the live surface before concluding a restart or new code is needed. Three of these four sessions recommended a restart; none required one.
