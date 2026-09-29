Four items (work-ffd855dd, work-13e6717f, work-e103255e, work-aad52b54) shipped code but left the record open. Each predecessor session concluded it was blocked. None was.

1. supersede_contract is step-gated, not staleness-gated. It refuses at an external-effect step such as repair or execution with "contract recovery is available only for a stale workflow contract", and is admissible at the verify or acceptance checkpoint, where the core offers it as the Revise choice. Read the step before believing the message.

2. The operator question exists only after an investigation artifact. requireRecordedInvestigationArtifact (internal/store/workflow_operator.go) admits a confirm_premise question only when a work observation's refs resolve to a current Product Domain AND a different work item. File paths and pull request URLs do not satisfy it. Without the artifact, pending_operator_decision is null and the adapter misdirects the caller to read it anyway. Record the observation with a Domain id and a peer work id first.

3. confirm_premise needs every evidence kind bound, including artifact, even when the contract's required_evidence omits it. Its decision_context_digest changes on every recorded action, so read it immediately before the call.

4. A review lane refusing dispatch at an external-effect step is not a missing review route. docs/development-authority.md gives pull requests plus required checks the review and merge authority. Bind the merged PR as review evidence.

5. The workflow complete action moves the lifecycle itself (CD-0183 D3). The completion fold appends the terminal work.transitioned event in the same transaction (internal/store/workflow_completion.go). An earlier version of this lesson said a separate concord_work_transition.lifecycle call was required; that repair landed, and the second call is no longer needed.

6. Verdict evaluation_evidence must equal an already-bound immutable_subject_ref. Bind before the acceptance step, because bind_evidence leaves the intent list there.

General rule: when a refusal names a remedy, the remedy is usually real and cheap. Probe the live surface before concluding a restart or new code is needed. Three of these four sessions recommended a restart; none required one.