Four items (work-ffd855dd, work-13e6717f, work-e103255e, work-aad52b54) shipped code but left the record open. Each predecessor session concluded it was blocked. None was.

1. Read the current step, contract, and declared recovery intents before concluding that recovery is unavailable. Contract content changes use the ordinary record update; proof remains bound to its subject ([workflow-engine contract §4.1](../../law/specs/workflow-engine-contract.md#41-ordinary-contract-updates)).

2. `confirm_premise` records the authenticated coordinator; it carries no operator selection or question digest.

3. A review lane refusing dispatch at an external-effect step is not a missing review route. docs/managed-development-authority.md gives pull requests plus required checks the review and merge authority. Bind the merged PR as review evidence.

4. The workflow complete action moves the lifecycle itself (CD-0183 D3). The completion fold appends the terminal work.transitioned event in the same transaction (internal/store/workflow_completion.go).

5. Bind ordinary evidence with its producer watermark. Inspect declared recovery intents instead of assuming that a route is absent.

General rule: when a refusal names a remedy, the remedy is usually real and cheap. Probe the live surface before concluding a restart or new code is needed. Three of these four sessions recommended a restart; none required one.
